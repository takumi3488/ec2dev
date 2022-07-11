package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"gopkg.in/yaml.v3"
)

type EC2DescribeInstancesAPI interface {
	DescribeInstances(ctx context.Context,
		params *ec2.DescribeInstancesInput,
		optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

// インスタンス一覧を取得
func GetInstances(c context.Context, api EC2DescribeInstancesAPI, instanceID string) (types.Instance, error) {
	describeInstancesInput := &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}}
	res, err := api.DescribeInstances(c, describeInstancesInput)
	if err != nil {
		panic(err)
	}
	if len(res.Reservations) == 0 || len(res.Reservations[0].Instances) == 0 {
		return types.Instance{}, errors.New("no instances found")
	}
	instance := res.Reservations[0].Instances[0]
	return instance, nil
}

type EC2StartInstancesAPI interface {
	StartInstances(ctx context.Context,
		params *ec2.StartInstancesInput,
		optFns ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error)
}

// インスタンスを開始
func StartInstance(c context.Context, api EC2StartInstancesAPI, instanceID string) {
	input := &ec2.StartInstancesInput{
		InstanceIds: []string{
			instanceID,
		},
	}
	res, err := api.StartInstances(c, input)
	if err != nil {
		fmt.Println("Got an error starting the instance")
		fmt.Println(err)
		return
	}
	OutputChangedInstance(res.StartingInstances[0])

}

type EC2StopInstancesAPI interface {
	StopInstances(ctx context.Context,
		params *ec2.StopInstancesInput,
		optFns ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error)
}

// インスタンスを停止
func StopInstance(c context.Context, api EC2StopInstancesAPI, instanceID string) {
	input := &ec2.StopInstancesInput{
		InstanceIds: []string{
			instanceID,
		},
	}
	res, err := api.StopInstances(c, input)
	if err != nil {
		fmt.Println("Got an error stopping the instance")
		fmt.Println(err)
		return
	}
	OutputChangedInstance(res.StoppingInstances[0])
}

// 変更されたインスタンスIDと状態を出力
func OutputChangedInstance(instance types.InstanceStateChange) {
	fmt.Printf("The instance state has been successfully changed!\nInstance ID: %s\nState: %s\n", *instance.InstanceId, instance.CurrentState.Name)
}

type Settings struct {
	InstanceID string `yaml:"instance_id"`
	Region     string
	Name       string
	Credential string
	Port       string
	User       string
}

func main() {
	// 設定
	var instanceID string
	sc := bufio.NewScanner(os.Stdin)

	// 設定ファイルから設定を読み込み
	// 存在しなければ入力を要請
	home := os.Getenv("HOME")
	b, err := ioutil.ReadFile(fmt.Sprintf("%s/.ec2dev/config.yml", home))
	settings := Settings{}
	if err != nil {
		panic(err)
	}
	if err := yaml.Unmarshal(b, &settings); err != nil {
		panic(err)
	}
	instanceID = settings.InstanceID

	// インスタンスIDが無ければ終了
	if instanceID == "" {
		fmt.Println("You must supply an instance ID")
		return
	}

	// AWS CLIのデフォルト設定を読み込み
	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		panic("configuration error, " + err.Error())
	}

	// Regionが設定されていればデフォルトから上書き
	if settings.Region != "" {
		cfg.Region = settings.Region
	}

	client := ec2.NewFromConfig(cfg)

	// インスタンスを取得
	instance, err := GetInstances(context.TODO(), client, instanceID)
	if err != nil {
		panic(err)
	}
	state := instance.State.Name
	fmt.Printf("Instance ID: %s\nState: %s\n", *instance.InstanceId, state)

	// 変更先を取得
	var target types.InstanceStateName
	if state == "running" {
		target = "stopped"
	} else if state == "stopped" {
		target = "running"
	} else {
		return
	}

	// 状態変更の確認
	fmt.Printf("Change the state to \"%s\"?(Yn): ", target)
	sc.Scan()
	if strings.ToLower(strings.TrimRight(sc.Text(), "\n")) == "n" {
		return
	}

	// 状態を変更
	fmt.Printf("Changing the state to %s\n", target)
	if state == "running" {
		StopInstance(context.TODO(), client, instanceID)
	} else if state == "stopped" {
		StartInstance(context.TODO(), client, instanceID)
	}

	// 起動待ち
	fmt.Printf("Waiting for %s state.\n", target)
	for i := 0; i < 60; i++ {
		time.Sleep(time.Second * 2)
		instance, err = GetInstances(context.TODO(), client, instanceID)
		if err != nil && instance.State.Name == target {
			break
		}
	}
	fmt.Printf("Instance ID: %s\nState: %s\n", *instance.InstanceId, instance.State.Name)

	// 状態がrunningであれば接続メッセージを出力
	if target == "running" {

		// .ssh/configを書き換え
		sconfp := fmt.Sprintf("%s/.ssh/config", home)
		sshconf, err := ioutil.ReadFile(sconfp)
		if err != nil {
			panic(err)
		}
		flg := true
		out := ""
		for _, row := range strings.Split(string(sshconf), "\n") {
			if strings.HasPrefix(row, "Host ") {
				if strings.HasSuffix(row, settings.Name) {
					flg = false
				} else {
					flg = true
					out += row
					out += "\n"
				}
			} else if row == "" {
				out += "\n"
			} else if flg {
				out += row
				out += "\n"
			}
		}
		out += fmt.Sprintf("Host %s\n  User %s\n  HostName %s\n  LocalForward %s localhost:%s\n  IdentityFile %s\n  ServerAliveInterval 5\n  ExitOnForwardFailure yes\n",
			settings.Name,
			settings.User,
			*instance.PublicIpAddress,
			settings.Port,
			settings.Port,
			settings.Credential,
		)
		ioutil.WriteFile(sconfp, []byte(out), os.ModePerm)

		fmt.Printf("Run below command to connect vscode:\ncode --remote ssh-remote+%s\n", settings.Name)
	}
}
