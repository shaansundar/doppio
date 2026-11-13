package main

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/shaansundar/doppio/internal/models"
)

func newModelsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "models", Short: "Inspect the model registry and licenses"}
	var all, jsonOut bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List models, their licenses and availability",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			reg, err := models.Default()
			if err != nil {
				return err
			}
			var rows []models.Model
			for _, m := range reg.All() {
				if all || m.Status == models.StatusAvailable {
					rows = append(rows, m)
				}
			}
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tTASK\tSTATUS\tLICENSE\tCOMMERCIAL\tSIZE")
			for _, m := range rows {
				commercial := "yes"
				if !m.License.Commercial {
					commercial = "no (opt-in)"
				}
				if m.License.Gated {
					commercial += ", gated"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", m.ID, m.Task, m.Status, m.License.ID, commercial, humanBytes(m.SizeBytes))
			}
			return tw.Flush()
		},
	}
	list.Flags().BoolVar(&all, "all", false, "include planned models")
	list.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	cmd.AddCommand(list)
	return cmd
}

func humanBytes(n int64) string {
	switch {
	case n <= 0:
		return "-"
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	default:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
}
