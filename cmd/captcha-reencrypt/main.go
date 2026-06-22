package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"go-admin/internal"
	appcaptcha "go-admin/internal/captcha"
)

func main() {
	dryRun := flag.Bool("dry-run", true, "report changes without updating encrypted values")
	flag.Parse()

	app, err := internal.InitializeApp()
	if err != nil {
		exitError(err)
	}
	if app.EntClient != nil {
		defer app.EntClient.Close()
	}
	if app.DB != nil {
		defer app.DB.Close()
	}

	report, err := appcaptcha.ReencryptProviderConfigs(
		context.Background(),
		app.EntClient,
		os.Getenv,
		*dryRun,
	)
	if err != nil {
		exitError(err)
	}
	output, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		exitError(err)
	}
	fmt.Println(string(output))
}

func exitError(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
