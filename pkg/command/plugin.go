// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package command

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aunum/log"
	"github.com/fatih/color"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	cliv1alpha1 "github.com/vmware-tanzu/tanzu-framework/apis/cli/v1alpha1"
	"github.com/vmware-tanzu/tanzu-plugin-runtime/component"
	"github.com/vmware-tanzu/tanzu-plugin-runtime/config"
	"github.com/vmware-tanzu/tanzu-plugin-runtime/plugin"

	"github.com/vmware-tanzu/tanzu-cli/pkg/cli"
	"github.com/vmware-tanzu/tanzu-cli/pkg/common"
	cliconfig "github.com/vmware-tanzu/tanzu-cli/pkg/config"
	"github.com/vmware-tanzu/tanzu-cli/pkg/discovery"
	"github.com/vmware-tanzu/tanzu-cli/pkg/pluginmanager"
)

var (
	local        string
	version      string
	forceDelete  bool
	outputFormat string
	targetStr    string
)

func newPluginCmd() *cobra.Command {
	var pluginCmd = &cobra.Command{
		Use:   "plugin",
		Short: "Manage CLI plugins",
		Annotations: map[string]string{
			"group": string(plugin.SystemCmdGroup),
		},
	}

	pluginCmd.SetUsageFunc(cli.SubCmdUsageFunc)

	listPluginCmd := newListPluginCmd()
	installPluginCmd := newInstallPluginCmd()
	upgradePluginCmd := newUpgradePluginCmd()
	describePluginCmd := newDescribePluginCmd()
	deletePluginCmd := newDeletePluginCmd()
	cleanPluginCmd := newCleanPluginCmd()
	syncPluginCmd := newSyncPluginCmd()
	discoverySourceCmd := newDiscoverySourceCmd()

	listPluginCmd.Flags().StringVarP(&outputFormat, "output", "o", "", "Output format (yaml|json|table)")
	listPluginCmd.Flags().StringVarP(&local, "local", "l", "", "path to local plugin source")
	installPluginCmd.Flags().StringVarP(&local, "local", "l", "", "path to local discovery/distribution source")
	installPluginCmd.Flags().StringVarP(&version, "version", "v", cli.VersionLatest, "version of the plugin")
	deletePluginCmd.Flags().BoolVarP(&forceDelete, "yes", "y", false, "delete the plugin without asking for confirmation")

	if config.IsFeatureActivated(cliconfig.FeatureContextCommand) {
		installPluginCmd.Flags().StringVarP(&targetStr, "target", "t", "", "target of the plugin (kubernetes[k8s]/mission-control[tmc])")
		upgradePluginCmd.Flags().StringVarP(&targetStr, "target", "t", "", "target of the plugin (kubernetes[k8s]/mission-control[tmc])")
		deletePluginCmd.Flags().StringVarP(&targetStr, "target", "t", "", "target of the plugin (kubernetes[k8s]/mission-control[tmc])")
		describePluginCmd.Flags().StringVarP(&targetStr, "target", "t", "", "target of the plugin (kubernetes[k8s]/mission-control[tmc])")
	}

	pluginCmd.AddCommand(
		listPluginCmd,
		installPluginCmd,
		upgradePluginCmd,
		describePluginCmd,
		deletePluginCmd,
		cleanPluginCmd,
		syncPluginCmd,
		discoverySourceCmd,
	)
	return pluginCmd
}

func newListPluginCmd() *cobra.Command {
	var listCmd = &cobra.Command{
		Use:   "list",
		Short: "List available plugins",
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			var availablePlugins []discovery.Discovered
			if local != "" {
				// get absolute local path
				local, err = filepath.Abs(local)
				if err != nil {
					return err
				}
				availablePlugins, err = pluginmanager.AvailablePluginsFromLocalSource(local)
			} else {
				availablePlugins, err = pluginmanager.AvailablePlugins()
			}

			if err != nil {
				return err
			}
			sort.Sort(discovery.DiscoveredSorter(availablePlugins))

			if config.IsFeatureActivated(cliconfig.FeatureContextCommand) && (outputFormat == "" || outputFormat == string(component.TableOutputType)) {
				displayPluginListOutputSplitViewContext(availablePlugins, cmd.OutOrStdout())
			} else {
				displayPluginListOutputListView(availablePlugins, cmd.OutOrStdout())
			}

			return nil
		},
	}

	return listCmd
}

func newDescribePluginCmd() *cobra.Command {
	var describeCmd = &cobra.Command{
		Use:   "describe [name]",
		Short: "Describe a plugin",
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			if len(args) != 1 {
				return fmt.Errorf("must provide plugin name as positional argument")
			}
			pluginName := args[0]

			if !cliv1alpha1.IsValidTarget(targetStr) {
				return errors.New("invalid target specified. Please specify correct value of `--target` or `-t` flag from 'kubernetes/k8s/mission-control/tmc'")
			}

			pd, err := pluginmanager.DescribePlugin(pluginName, getTarget())
			if err != nil {
				return err
			}

			b, err := yaml.Marshal(pd)
			if err != nil {
				return errors.Wrap(err, "could not marshal plugin")
			}
			fmt.Println(string(b))
			return nil
		},
	}

	return describeCmd
}

func newInstallPluginCmd() *cobra.Command {
	var installCmd = &cobra.Command{
		Use:   "install [name]",
		Short: "Install a plugin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error

			pluginName := args[0]

			if !cliv1alpha1.IsValidTarget(targetStr) {
				return errors.New("invalid target specified. Please specify correct value of `--target` or `-t` flag from 'kubernetes/k8s/mission-control/tmc'")
			}

			// Invoke install plugin from local source if local files are provided
			if local != "" {
				// get absolute local path
				local, err = filepath.Abs(local)
				if err != nil {
					return err
				}
				err = pluginmanager.InstallPluginsFromLocalSource(pluginName, version, getTarget(), local, false)
				if err != nil {
					return err
				}
				if pluginName == cli.AllPlugins {
					log.Successf("successfully installed all plugins")
				} else {
					log.Successf("successfully installed '%s' plugin", pluginName)
				}
				return nil
			}

			// Invoke plugin sync if install all plugins is mentioned
			if pluginName == cli.AllPlugins {
				err = pluginmanager.SyncPlugins()
				if err != nil {
					return err
				}
				log.Successf("successfully installed all plugins")
				return nil
			}

			pluginVersion := version
			if pluginVersion == cli.VersionLatest {
				pluginVersion, err = pluginmanager.GetRecommendedVersionOfPlugin(pluginName, getTarget())
				if err != nil {
					return err
				}
			}

			err = pluginmanager.InstallPlugin(pluginName, pluginVersion, getTarget())
			if err != nil {
				return err
			}
			log.Successf("successfully installed '%s' plugin", pluginName)
			return nil
		},
	}

	return installCmd
}

func newUpgradePluginCmd() *cobra.Command {
	var upgradeCmd = &cobra.Command{
		Use:   "upgrade [name]",
		Short: "Upgrade a plugin",
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			if len(args) != 1 {
				return fmt.Errorf("must provide plugin name as positional argument")
			}
			pluginName := args[0]

			if !cliv1alpha1.IsValidTarget(targetStr) {
				return errors.New("invalid target specified. Please specify correct value of `--target` or `-t` flag from 'kubernetes/k8s/mission-control/tmc'")
			}

			pluginVersion, err := pluginmanager.GetRecommendedVersionOfPlugin(pluginName, getTarget())
			if err != nil {
				return err
			}

			err = pluginmanager.UpgradePlugin(pluginName, pluginVersion, getTarget())
			if err != nil {
				return err
			}
			log.Successf("successfully upgraded plugin '%s' to version '%s'", pluginName, pluginVersion)
			return nil
		},
	}

	return upgradeCmd
}

func newDeletePluginCmd() *cobra.Command {
	var deleteCmd = &cobra.Command{
		Use:   "delete [name]",
		Short: "Delete a plugin",
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			if len(args) != 1 {
				return fmt.Errorf("must provide plugin name as positional argument")
			}
			pluginName := args[0]

			if !cliv1alpha1.IsValidTarget(targetStr) {
				return errors.New("invalid target specified. Please specify correct value of `--target` or `-t` flag from 'kubernetes/k8s/mission-control/tmc'")
			}

			deletePluginOptions := pluginmanager.DeletePluginOptions{
				PluginName:  pluginName,
				Target:      getTarget(),
				ForceDelete: forceDelete,
			}

			err = pluginmanager.DeletePlugin(deletePluginOptions)
			if err != nil {
				return err
			}

			log.Successf("successfully deleted plugin '%s'", pluginName)
			return nil
		},
	}
	return deleteCmd
}

func newCleanPluginCmd() *cobra.Command {
	var cleanCmd = &cobra.Command{
		Use:   "clean",
		Short: "Clean the plugins",
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			err = pluginmanager.Clean()
			if err != nil {
				return err
			}
			log.Success("successfully cleaned up all plugins")
			return nil
		},
	}
	return cleanCmd
}

func newSyncPluginCmd() *cobra.Command {
	var syncCmd = &cobra.Command{
		Use:   "sync",
		Short: "Sync the plugins",
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			err = pluginmanager.SyncPlugins()
			if err != nil {
				return err
			}
			log.Success("Done")
			return nil
		},
	}
	return syncCmd
}

// getInstalledElseAvailablePluginVersion return installed plugin version if plugin is installed
// if not installed it returns available recommended plugin version
func getInstalledElseAvailablePluginVersion(p *discovery.Discovered) string {
	installedOrAvailableVersion := p.InstalledVersion
	if installedOrAvailableVersion == "" {
		installedOrAvailableVersion = p.RecommendedVersion
	}
	return installedOrAvailableVersion
}

func displayPluginListOutputListView(availablePlugins []discovery.Discovered, writer io.Writer) {
	var data [][]string
	var output component.OutputWriter

	for index := range availablePlugins {
		data = append(data, []string{availablePlugins[index].Name, availablePlugins[index].Description, availablePlugins[index].Scope,
			availablePlugins[index].Source, getInstalledElseAvailablePluginVersion(&availablePlugins[index]), availablePlugins[index].Status})
	}
	output = component.NewOutputWriter(writer, outputFormat, "Name", "Description", "Scope", "Discovery", "Version", "Status")

	for _, row := range data {
		vals := make([]interface{}, len(row))
		for i, val := range row {
			vals[i] = val
		}
		output.AddRow(vals...)
	}
	output.Render()
}

func displayPluginListOutputSplitViewContext(availablePlugins []discovery.Discovered, writer io.Writer) {
	var dataStandalone [][]string
	var outputStandalone component.OutputWriter
	dataContext := make(map[string][][]string)
	outputContext := make(map[string]component.OutputWriter)

	outputStandalone = component.NewOutputWriter(writer, outputFormat, "Name", "Description", "Target", "Discovery", "Version", "Status")

	for index := range availablePlugins {
		if availablePlugins[index].Scope == common.PluginScopeStandalone {
			newRow := []string{availablePlugins[index].Name, availablePlugins[index].Description, string(availablePlugins[index].Target),
				availablePlugins[index].Source, getInstalledElseAvailablePluginVersion(&availablePlugins[index]), availablePlugins[index].Status}
			dataStandalone = append(dataStandalone, newRow)
		} else {
			newRow := []string{availablePlugins[index].Name, availablePlugins[index].Description, string(availablePlugins[index].Target),
				getInstalledElseAvailablePluginVersion(&availablePlugins[index]), availablePlugins[index].Status}
			outputContext[availablePlugins[index].ContextName] = component.NewOutputWriter(writer, outputFormat, "Name", "Description", "Target", "Version", "Status")
			data := dataContext[availablePlugins[index].ContextName]
			data = append(data, newRow)
			dataContext[availablePlugins[index].ContextName] = data
		}
	}

	addDataToOutputWriter := func(output component.OutputWriter, data [][]string) {
		for _, row := range data {
			vals := make([]interface{}, len(row))
			for i, val := range row {
				vals[i] = val
			}
			output.AddRow(vals...)
		}
	}

	cyanBold := color.New(color.FgCyan).Add(color.Bold)
	cyanBoldItalic := color.New(color.FgCyan).Add(color.Bold, color.Italic)

	_, _ = cyanBold.Println("Standalone Plugins")
	addDataToOutputWriter(outputStandalone, dataStandalone)
	outputStandalone.Render()

	for context, writer := range outputContext {
		fmt.Println("")
		_, _ = cyanBold.Println("Plugins from Context: ", cyanBoldItalic.Sprintf(context))
		data := dataContext[context]
		addDataToOutputWriter(writer, data)
		writer.Render()
	}
}

func getTarget() cliv1alpha1.Target {
	return cliv1alpha1.StringToTarget(strings.ToLower(targetStr))
}