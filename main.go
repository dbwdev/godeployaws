package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/gorilla/mux"

	"context"
	"encoding/json"
	"io"
	"io/ioutil"
	"log"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/joho/godotenv"
)

func main() {
	router := mux.NewRouter()

	router.HandleFunc("/api/lambda/deploy", DeployLambda).Methods("POST")

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	fmt.Println(port)

	err := http.ListenAndServe(":"+port, router)
	if err != nil {
		fmt.Print(err)
	}
}

type lambdaStruct struct {
	FuncDef string
}

// DeployLambda deploys lambda to AWS
var DeployLambda = func(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)
	var l lambdaStruct
	err := decoder.Decode(&l)
	if err != nil {
		panic(err)
	}
	fmt.Println(l.FuncDef)

	ioutil.WriteFile("functions/handler.js", []byte(l.FuncDef), 0666)

	conf := getEnvVariables()
	createNewContainer(conf)
}

type awsConf struct {
	accessKey     string
	secretKey     string
	defaultRegion string
}

func getEnvVariables() awsConf {
	if os.Getenv("APP_ENV") != "production" {
		err := godotenv.Load()
		if err != nil {
			log.Fatal("Error loading .env file")
		}
	}

	a := os.Getenv("AWS_ACCESS_KEY_ID")
	s := os.Getenv("AWS_SECRET_ACCESS_KEY")
	r := os.Getenv("AWS_DEFAULT_REGION")

	conf := awsConf{accessKey: a, secretKey: s, defaultRegion: r}
	return conf
}

func createNewContainer(conf awsConf) (string, error) {
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
			Env:          []string{"AWS_ACCESS_KEY_ID=" + conf.accessKey, "AWS_SECRET_ACCESS_KEY=" + conf.secretKey, "AWS_DEFAULT_REGION=" + conf.defaultRegion},
			Cmd:          []string{"/bin/sh", "-c", "sam package --template-file template.yml --s3-bucket sam-test-bucket-aox --output-template-file packaged-template.yaml && sam deploy --region us-east-1 --template-file packaged-template.yaml --stack-name l9-hello --capabilities CAPABILITY_IAM"},
			Tty:          true,
			AttachStdout: true,
			AttachStderr: true,
		}, &container.HostConfig{
			Mounts: []mount.Mount{
				{
					Type:   mount.TypeBind,
					Source: "/Users/bruce/Documents/Code/go-docker/functions",
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

