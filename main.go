package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
)
func main() {
	router := mux.NewRouter()
	router.HandleFunc("/lambda/deploy", DeployLambda).Methods("POST")

	port := os.Getenv("PORT")
	if port == "" {
		port = "80"
	}

	fmt.Println("Running on " + port)
	err := http.ListenAndServe(":"+port, router)
	if err != nil {
		fmt.Print(err)
	}
}

type Lambda struct {
	FuncName string
	FuncDef string
}

type LambdaReq struct {
	FuncArr []Lambda
	YAML string
}

type Conf struct {
	AccessKey     string
	SecretKey     string
	DefaultRegion string
}

// DeployLambda deploys lambda to AWS
var DeployLambda = func(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)
	var lr LambdaReq
	err := decoder.Decode(&lr)
	if err != nil {
		panic(err)
	}

	fmt.Println(lr.YAML)
	fmt.Println(lr.FuncArr)
	writeYaml(lr.YAML)
	writeFunctions(lr.FuncArr)

	conf := getEnvVariables()
	_, err = createNewContainer(conf)

	removeFunctions("/go/src/github.com/go-deploy/functions")

	message(200, "Successfully deployed to AWS lambda")
}

func message(statusCode int, message string) map[string]interface{} {
	return map[string]interface{}{"status": statusCode, "message": message}
}

func getEnvVariables() Conf {
	if os.Getenv("APP_ENV") != "production" {
		err := godotenv.Load()
		if err != nil {
			log.Fatal("Error loading .env file")
		}
	}

	a := os.Getenv("AWS_ACCESS_KEY_ID")
	s := os.Getenv("AWS_SECRET_ACCESS_KEY")
	r := os.Getenv("AWS_DEFAULT_REGION")

	conf := Conf{AccessKey: a, SecretKey: s, DefaultRegion: r}
	return conf
}

func writeYaml(yamlStr string) error {
	return ioutil.WriteFile("/go/src/github.com/go-deploy/functions/template.yml", []byte(yamlStr), 0666)
}

func writeFunctions(fns []Lambda) {
		for _, fn := range fns {
			fmt.Println(fn.FuncName)
			ioutil.WriteFile("/go/src/github.com/go-deploy/functions/"+fn.FuncName, []byte(fn.FuncDef), 0666);
		}
	}

func removeFunctions(dirName string) {
	dir, err := ioutil.ReadDir(dirName)
	if err != nil {
		log.Fatal("Error removing functions")
	}
	for _, d := range dir {
		fmt.Println("Removing "+d.Name())
		os.RemoveAll(path.Join([]string{dirName, d.Name()}...))
	}
}

func createNewContainer(conf Conf) (string, error) {
	ctx := context.Background()
	cli, err := client.NewEnvClient()
	cli.NegotiateAPIVersion(ctx)
	if err != nil {
		fmt.Println("Unable to create docker client")
		panic(err)
	}

	containerPort, err := nat.NewPort("tcp", "80")
	if err != nil {
		log.Println("Unable to get newPort")
		return "", err
	}

	hostBinding := nat.PortBinding{
		HostIP:   "0.0.0.0",
		HostPort: "8000",
	}

	portBinding := nat.PortMap{containerPort: []nat.PortBinding{hostBinding}}

	reader, err := cli.ImagePull(ctx, "dbwdev/aws-cli-sam", types.ImagePullOptions{})
	if err != nil {
		panic(err)
	}

	io.Copy(os.Stdout, reader)

	resp, err := cli.ContainerCreate(
		ctx,
		&container.Config{
			Image:        "dbwdev/aws-cli-sam",
			WorkingDir:   "/usr/app",
			Env:          []string{"AWS_ACCESS_KEY_ID=" + conf.AccessKey, "AWS_SECRET_ACCESS_KEY=" + conf.SecretKey, "AWS_DEFAULT_REGION=" + conf.DefaultRegion},
			Cmd:          []string{"/bin/sh", "-c", "sam package --template-file template.yml --s3-bucket sam-test-bucket-aox --output-template-file packaged-template.yaml && sam deploy --region us-east-1 --template-file packaged-template.yaml --stack-name l9-hello --capabilities CAPABILITY_IAM"},
			Tty:          true,
			AttachStdout: true,
			AttachStderr: true,
		}, &container.HostConfig{
			Mounts: []mount.Mount{
				{
					Type:   mount.TypeBind,
					Source: "/home/ec2-user/functions",
					Target: "/usr/app",
				},
			},
			PortBindings: portBinding,
		}, nil, "")
	if err != nil {
		panic(err)
	}

	if err := cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		panic(err)
	}

	statusCh, errCh := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			panic(err)
		}
	case <-statusCh:
	}

	out, err := cli.ContainerLogs(ctx, resp.ID, types.ContainerLogsOptions{ShowStdout: true, ShowStderr: true,
		Follow:     true,
		Timestamps: false})
	if err != nil {
		panic(err)
	}

	defer out.Close()

	io.Copy(os.Stdout, out)

	return resp.ID, nil
}
