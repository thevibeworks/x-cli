package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Inspect CLI configuration",
}

var configGetCmd = &cobra.Command{
	Use:   "get [key]",
	Short: "Print a config value (or all values if no key)",
	RunE:  runConfigGet,
}

var configPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the loaded config file path",
	RunE: func(*cobra.Command, []string) error {
		p := viper.ConfigFileUsed()
		if p == "" {
			fmt.Fprintln(os.Stderr, "(no config file loaded)")
			return nil
		}
		fmt.Println(p)
		return nil
	},
}

func init() {
	configCmd.AddCommand(configGetCmd)
	configCmd.AddCommand(configPathCmd)
	rootCmd.AddCommand(configCmd)
}

func runConfigGet(_ *cobra.Command, args []string) error {
	if len(args) == 0 {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(viper.AllSettings())
	}
	v := viper.Get(args[0])
	if v == nil {
		return fmt.Errorf("not set: %s", args[0])
	}
	fmt.Println(v)
	return nil
}
