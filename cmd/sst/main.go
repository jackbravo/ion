package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"reflect"
	"strings"

	//"syscall"
	"time"

	"github.com/briandowns/spinner"
	"github.com/fatih/color"
	"github.com/manifoldco/promptui"

	"github.com/joho/godotenv"
	"github.com/sst/ion/cmd/sst/ui"
	"github.com/sst/ion/internal/util"
	"github.com/sst/ion/pkg/global"
	"github.com/sst/ion/pkg/project"
	"github.com/sst/ion/pkg/project/provider"
	"github.com/sst/ion/pkg/server"

	cli "github.com/urfave/cli/v2"
)

var version = "dev"

var logFile = (func() *os.File {
	logFile, err := os.CreateTemp("", "sst-*.log")
	if err != nil {
		panic(err)
	}
	return logFile
})()

func main() {
	godotenv.Load()
	interruptChannel := make(chan os.Signal, 1)
	signal.Notify(interruptChannel, os.Interrupt)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-interruptChannel
		cancel()
	}()

	app := &cli.App{
		Name:        "sst",
		Description: "deploy anything",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name: "verbose",
			},
			&cli.StringFlag{
				Name: "stage",
			},
		},
		Before: func(c *cli.Context) error {
			configureLog(c)

			spin := spinner.New(spinner.CharSets[14], 100*time.Millisecond)
			spin.Suffix = "  First run, setting up environment..."

			if global.NeedsPulumi() {
				spin.Start()
				err := global.InstallPulumi()
				if err != nil {
					return err
				}
			}

			if global.NeedsPlugins() {
				spin.Start()
				err := global.InstallPlugins()
				if err != nil {
					return err
				}
			}

			spin.Stop()

			return nil
		},
		Commands: []*cli.Command{
			{
				Name:  "version",
				Flags: []cli.Flag{},
				Action: func(cli *cli.Context) error {
					fmt.Printf("ion.%s\n", version)
					return nil
				},
			},
			{
				Name: "secrets",
				Subcommands: []*cli.Command{
					{
						Name:      "set",
						ArgsUsage: "[key] [value]",
						Action: func(cli *cli.Context) error {
							p, err := initProject(cli)
							if err != nil {
								return err
							}
							if cli.Args().Len() != 2 {
								return fmt.Errorf("key and value required")
							}

							key := cli.Args().Get(0)
							value := cli.Args().Get(1)
							backend := p.Backend()
							secrets, err := provider.GetSecrets(backend, p.App().Name, p.App().Stage)
							if err != nil {
								return err
							}
							secrets[key] = value
							err = provider.PutSecrets(backend, p.App().Name, p.App().Stage, secrets)
							if err != nil {
								return err
							}
							fmt.Println("Secret set")
							return nil
						},
					},
					{
						Name: "list",
						Action: func(cli *cli.Context) error {
							p, err := initProject(cli)
							if err != nil {
								return err
							}
							backend := p.Backend()
							secrets, err := provider.GetSecrets(backend, p.App().Name, p.App().Stage)
							if err != nil {
								return err
							}
							for key, value := range secrets {
								fmt.Println(key, "=", value)
							}
							return nil
						},
					},
				},
			},
			{
				Name:  "link",
				Flags: []cli.Flag{},
				Action: func(cli *cli.Context) error {
					p, err := initProject(cli)
					if err != nil {
						return err
					}
					backend := p.Backend()
					links, err := provider.GetLinks(backend, p.App().Name, p.App().Stage)
					if err != nil {
						return err
					}
					args := cli.Args().Slice()
					cmd := exec.Command(
						args[0],
						args[1:]...,
					)
					cmd.Env = append(cmd.Env,
						os.Environ()...,
					)

					for resource, value := range links {
						jsonValue, err := json.Marshal(value)
						if err != nil {
							return err
						}

						envVar := fmt.Sprintf("SST_RESOURCE_%s=%s", resource, jsonValue)
						cmd.Env = append(cmd.Env, envVar)
					}
					cmd.Stdout = os.Stdout
					cmd.Stderr = os.Stderr
					return cmd.Run()
				},
			},
			{
				Name:  "server",
				Flags: []cli.Flag{},
				Action: func(cli *cli.Context) error {
					project, err := initProject(cli)
					if err != nil {
						return err
					}

					server, err := server.New(project)
					if err != nil {
						return err
					}
					interruptChannel := make(chan os.Signal, 1)
					signal.Notify(interruptChannel, os.Interrupt)

					err = server.Start(ctx)
					if err != nil {
						return err
					}
					return nil
				},
			},
			{
				Name:  "dev",
				Flags: []cli.Flag{},
				Action: func(cli *cli.Context) error {
					args := cli.Args().Slice()
					hasTarget := len(args) > 0

					cfgPath, err := project.Discover()
					if err != nil {
						return err
					}

					stage, err := getStage(cli, cfgPath)
					if err != nil {
						return err
					}

					restartTarget := make(chan project.Links)
					runOnce := false
					go func() {
						if !hasTarget {
							return
						}
						links := <-restartTarget

						for {
							cmd := exec.Command(
								args[0],
								args[1:]...,
							)

							cmd.Env = append(cmd.Env,
								os.Environ()...,
							)

							for resource, value := range links {
								jsonValue, _ := json.Marshal(value)
								envVar := fmt.Sprintf("SST_RESOURCE_%s=%s", resource, jsonValue)
								cmd.Env = append(cmd.Env, envVar)
							}

							cmd.Stdin = os.Stdin
							cmd.Stdout = os.Stdout
							cmd.Stderr = os.Stderr
							cmd.Start()
							runOnce = true
							processExit := make(chan interface{})

							go func() {
								cmd.Wait()
								processExit <- true
							}()

						loop:
							for {
								select {
								case <-ctx.Done():
									cmd.Process.Signal(os.Interrupt)
									return
								case <-processExit:
									cancel()
								case nextLinks := <-restartTarget:
									for key, value := range nextLinks {
										oldValue := links[key]
										if !reflect.DeepEqual(oldValue, value) {
											cmd.Process.Signal(os.Interrupt)
											cmd.Wait()
											fmt.Println("Restarting...")
											break loop
										}
									}
									continue
								}
							}
						}
					}()

					state := &server.State{}
					u := ui.New(ui.ProgressModeDev)
					err = server.Connect(cli.Context, server.ConnectInput{
						CfgPath: cfgPath,
						Stage:   stage,
						OnEvent: func(event server.Event) {
							if !hasTarget || !runOnce {
								defer u.Trigger(&event.StackEvent)
								if event.StackEvent.PreludeEvent != nil {
									u.Reset()
									u.Start()
									u.Changes()
								}
							}

							if event.PreludeEvent != nil && hasTarget && runOnce {
								fmt.Println()
								fmt.Println("🔥 SST is deploying, run sst dev to view progress 🔥")
								return
							}

							if event.CompleteEvent != nil {
								if hasTarget {
									if !runOnce && (!event.CompleteEvent.Finished || len(event.CompleteEvent.Errors) > 0) {
										cancel()
										return
									}

									restartTarget <- event.CompleteEvent.Links
								}
							}

							if event.StateEvent != nil {
								next := event.StateEvent.State
								defer func() {
									state = next
								}()

								if state.App == "" && next.App != "" {
									u.Header(
										version,
										next.App,
										next.Stage,
									)
								}
							}
						},
					})

					if err != nil {
						return err
					}

					return nil
				},
			},
			{
				Name:  "deploy",
				Flags: []cli.Flag{},
				Action: func(cli *cli.Context) error {
					p, err := initProject(cli)
					if err != nil {
						return err
					}
					ui := ui.New(ui.ProgressModeDeploy)
					defer ui.Destroy()
					ui.Header(version, p.App().Name, p.App().Stage)

					ctx, cancel := context.WithCancel(cli.Context)
					go func() {
						<-interruptChannel
						ui.Interrupt()
						cancel()
					}()
					ui.Start()
					err = p.Stack.Run(ctx, &project.StackInput{
						Command: "up",
						OnEvent: ui.Trigger,
					})
					if err != nil {
						return err
					}
					return nil
				},
			},
			{
				Name:  "remove",
				Flags: []cli.Flag{},
				Action: func(cli *cli.Context) error {
					p, err := initProject(cli)
					if err != nil {
						return err
					}
					ui := ui.New(ui.ProgressModeRemove)
					defer ui.Destroy()
					ui.Header(version, p.App().Name, p.App().Stage)

					interruptChannel := make(chan os.Signal, 1)
					signal.Notify(interruptChannel, os.Interrupt)

					ctx, cancel := context.WithCancel(cli.Context)
					go func() {
						<-interruptChannel
						ui.Interrupt()
						cancel()
					}()

					err = p.Stack.Run(ctx, &project.StackInput{
						Command: "destroy",
						OnEvent: ui.Trigger,
					})
					if err != nil {
						return err
					}
					return nil
				},
			},
			{
				Name:  "refresh",
				Flags: []cli.Flag{},
				Action: func(cli *cli.Context) error {
					p, err := initProject(cli)
					if err != nil {
						return err
					}
					ui := ui.New(ui.ProgressModeRefresh)
					defer ui.Destroy()
					ui.Header(version, p.App().Name, p.App().Stage)

					interruptChannel := make(chan os.Signal, 1)
					signal.Notify(interruptChannel, os.Interrupt)

					ctx, cancel := context.WithCancel(cli.Context)
					go func() {
						<-interruptChannel
						ui.Interrupt()
						cancel()
					}()

					err = p.Stack.Run(ctx, &project.StackInput{
						Command: "refresh",
						OnEvent: ui.Trigger,
					})
					if err != nil {
						return err
					}
					return nil
				},
			},
			{
				Name:  "cancel",
				Flags: []cli.Flag{},
				Action: func(cli *cli.Context) error {
					p, err := initProject(cli)
					if err != nil {
						return err
					}

					err = p.Stack.Cancel()
					if err != nil {
						return err
					}

					fmt.Println("Cancelled any pending deploys for", p.App().Name, "/", p.App().Stage)

					return nil
				},
			},
			{
				Name:  "create",
				Flags: []cli.Flag{},
				Action: func(cli *cli.Context) error {

					if _, err := os.Stat("sst.config.ts"); err == nil {
						color.New(color.FgRed, color.Bold).Print("❌")
						color.New(color.FgWhite, color.Bold).Println(" sst.config.ts already exists")
						return nil
					}

					template := "vanilla"

					if _, err := os.Stat("next.config.js"); err == nil {
						p := promptui.Select{
							Label:        "Next.js detected, would you like to use the Next.js template?",
							HideSelected: true,
							Items:        []string{"Yes", "No"},
							HideHelp:     true,
						}
						_, result, err := p.Run()
						if err != nil {
							return err
						}

						if result == "Yes" {
							template = "nextjs"
						}
					}

					err := project.Create(template)
					if err != nil {
						return err
					}

					cfgPath, err := project.Discover()
					if err != nil {
						return err
					}

					if !project.CheckDeps(version, cfgPath) {
						spin := spinner.New(spinner.CharSets[14], 100*time.Millisecond)
						spin.Suffix = "  Installing dependencies..."
						spin.Start()
						err = project.InstallDeps(version, cfgPath)
						spin.Stop()
						if err != nil {
							return err
						}
					}

					color.New(color.FgGreen, color.Bold).Print("✔")
					color.New(color.FgWhite, color.Bold).Println("  Created new project with '", template, "' template")

					return nil

				},
			},
		},
	}

	err := app.RunContext(ctx, os.Args)
	if err != nil {
		if readableErr, ok := err.(*util.ReadableError); ok {
			fmt.Println(readableErr.Message)
		} else {
			panic(err)
		}
	}

}

func getStage(cli *cli.Context, cfgPath string) (string, error) {
	stage := cli.String("stage")
	if stage == "" {
		stage = project.LoadPersonalStage(cfgPath)
		if stage == "" {
			stage = guessStage()
			if stage == "" {
				for {
					fmt.Print("Enter a stage name for your personal stage: ")
					_, err := fmt.Scanln(&stage)
					if err != nil {
						continue
					}
					if stage == "" {
						continue
					}
					break
				}
			}
			err := project.SetPersonalStage(cfgPath, stage)
			if err != nil {
				return "", err
			}
		}
	}
	godotenv.Load(fmt.Sprintf(".env.%s", stage))
	return stage, nil
}

func initProject(cli *cli.Context) (*project.Project, error) {
	slog.Info("initializing project", "version", version)

	cfgPath, err := project.Discover()
	if err != nil {
		return nil, err
	}

	stage, err := getStage(cli, cfgPath)
	if err != nil {
		return nil, err
	}

	if !project.CheckDeps(version, cfgPath) {
		spin := spinner.New(spinner.CharSets[14], 100*time.Millisecond)
		spin.Suffix = "  Installing dependencies..."
		spin.Start()
		err = project.InstallDeps(version, cfgPath)
		if err != nil {
			return nil, err
		}
		spin.Stop()
	}

	p, err := project.New(&project.ProjectConfig{
		Version: version,
		Stage:   stage,
		Config:  cfgPath,
	})
	if err != nil {
		return nil, err
	}

	_, err = logFile.Seek(0, 0)
	if err != nil {
		return nil, err
	}
	nextLogFile, err := os.Create(filepath.Join(p.PathWorkingDir(), "sst.log"))
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(nextLogFile, logFile)
	if err != nil {
		return nil, err
	}
	logFile = nextLogFile
	configureLog(cli)

	app := p.App()
	slog.Info("loaded config", "app", app.Name, "stage", app.Stage)

	return p, nil
}

func configureLog(cli *cli.Context) {
	writers := []io.Writer{logFile}
	if cli.Bool("verbose") {
		writers = append(writers, os.Stderr)
	}
	writer := io.MultiWriter(writers...)
	slog.SetDefault(
		slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})),
	)
}

func guessStage() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	stage := strings.ToLower(u.Username)

	if stage == "root" || stage == "admin" || stage == "prod" || stage == "dev" || stage == "production" {
		return ""
	}

	return stage
}
