package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/olusolaa/infra-drift-detector/internal/app"
	apperrors "github.com/olusolaa/infra-drift-detector/internal/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	cfgFile   string
	logLevel  string
	logFormat string
	// Bonus point: CLI flag to specify attributes per kind
	// Format: kind1=attr1,attr2;kind2=attrA,attrB
	attributesOverride string
)

var rootCmd = &cobra.Command{
	Use:   "drift-analyser",
	Short: "Detects infrastructure drift between desired state and actual platform state.",
	Long: `Drift Analyser compares resources defined in a desired state source
(like Terraform state) against their actual configuration on a cloud platform (like AWS)
and reports any detected drift based on configured attributes.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return initializeConfig(cmd)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		application, bootstrapErr := app.BuildApplicationFromViper(cmd.Context(), viper.GetViper())
		if bootstrapErr != nil {
			fmt.Fprintf(os.Stderr, "ERROR: Application initialization failed: %v\n", bootstrapErr)
			if appErr := (*apperrors.AppError)(nil); errors.As(bootstrapErr, &appErr) {
				if appErr.IsUserFacing {
					fmt.Fprintf(os.Stderr, "Error Details: %s\n", appErr.Message)
					if appErr.SuggestedAction != "" {
						fmt.Fprintf(os.Stderr, "Suggestion: %s\n", appErr.SuggestedAction)
					}
				}
			}
			return bootstrapErr
		}

		appLogger := application.Logger
		appCtx := cmd.Context()

		appLogger.Infof(appCtx, "Starting analysis engine run...")
		runErr := application.Engine.Run(appCtx)

		if runErr != nil {
			appLogger.Errorf(appCtx, runErr, "Application execution failed")

			userMsg, suggestion, _ := apperrors.GetUserFacingMessage(runErr)
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", userMsg)
			if suggestion != "" {
				fmt.Fprintf(os.Stderr, "Suggestion: %s\n", suggestion)
			}
			return runErr
		}

		appLogger.Infof(appCtx, "Application finished successfully.")
		return nil
	},
}

func Execute(ctx context.Context) {
	err := rootCmd.ExecuteContext(ctx)
	// could this be error that is recoverable?
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "Configuration file path (default is config.yaml or .drift-analyser.yaml)")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "", "Override log level (debug, info, warn, error)")
	rootCmd.PersistentFlags().StringVar(&logFormat, "log-format", "", "Override log format (text, json)")
	rootCmd.PersistentFlags().StringVar(&attributesOverride, "attributes", "", "Override attributes to check per kind (e.g., 'ComputeInstance=instance_type,tags;StorageBucket=acl')")

	viper.BindPFlag("settings.log_level", rootCmd.PersistentFlags().Lookup("log-level"))
	viper.BindPFlag("settings.log_format", rootCmd.PersistentFlags().Lookup("log-format"))

	viper.SetEnvPrefix("DRIFT")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()
}

func initializeConfig(cmd *cobra.Command) error {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		viper.AddConfigPath(".")
		viper.AddConfigPath(home)
		viper.SetConfigName(".drift-analyser")
		viper.SetConfigType("yaml")
	}

	if err := viper.ReadInConfig(); err == nil {
		fmt.Fprintln(os.Stderr, "Using configuration file:", viper.ConfigFileUsed())
	} else {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			fmt.Fprintln(os.Stderr, "Config file not found, using defaults and environment variables.")
		} else {
			return apperrors.Wrap(err, apperrors.CodeConfigReadError, "failed to read config file")
		}
	}

	return nil
}
