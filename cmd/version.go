package cmd

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
)

var (
	// Version defines the application version (defined at compile time)
	Version = ""
	Commit  = ""
	Dirty   = ""
)

type versionInfo struct {
	Version string `json:"version" yaml:"version"`
	Commit  string `json:"commit" yaml:"commit"`
	Go      string `json:"go" yaml:"go"`
}

func getVersionCmd() *cobra.Command {
	versionCmd := &cobra.Command{
		Use:     "version",
		Aliases: []string{"v"},
		Short:   "Print the relayer version info",
		Example: strings.TrimSpace(fmt.Sprintf(`
$ %s version --json
$ %s v`,
			appName, appName,
		)),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsn, err := cmd.Flags().GetBool(flagJSON)
			if err != nil {
				return err
			}

			commit := Commit
			if Dirty != "0" {
				commit += " (dirty)"
			}

			verInfo := versionInfo{
				Version: Version,
				Commit:  commit,
				Go:      fmt.Sprintf("%s %s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH),
			}

			var bz []byte
			if jsn {
				bz, err = json.Marshal(verInfo)
			} else {
				bz, err = yaml.Marshal(&verInfo)
			}

			fmt.Fprintln(cmd.OutOrStdout(), string(bz))
			return err
		},
	}
	return addJSONFlag(versionCmd)
}
