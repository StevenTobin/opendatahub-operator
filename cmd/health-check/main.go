// health-check runs cluster health checks and exits 0 if the cluster is healthy, 1 otherwise.
// Configuration can be set via flags or environment variables; flags take precedence.
// Use -help to see options and defaults (env vars are documented there).
//
// Subcommands:
//
//	health-check                       cluster health (default)
//	health-check validate manifests    validate manifest paths against build/manifests-config.yaml
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrl "sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/clusterhealth"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/validate"
)

const (
	envOperatorNamespace     = "E2E_TEST_OPERATOR_NAMESPACE"
	envApplicationsNamespace = "E2E_TEST_APPLICATIONS_NAMESPACE"
	envOperatorDeployment    = "E2E_TEST_OPERATOR_DEPLOYMENT_NAME"
	envMonitoringNamespace   = "E2E_TEST_DSC_MONITORING_NAMESPACE"
	defaultOperatorNS        = "opendatahub-operator-system"
	defaultAppsNS            = "opendatahub"
	defaultOperatorDeploy    = "opendatahub-operator-controller-manager"
	defaultMonitoringNS      = "opendatahub"
)

func main() {
	// Dispatch subcommand before parsing flags for the default cluster health path.
	if len(os.Args) >= 2 && os.Args[1] == "validate" {
		runValidateCmd(os.Args[2:])
		return
	}

	outputJSON := flag.Bool("json", false, "Output report as JSON")
	longFormat := flag.Bool("l", false, "Long format: list conditions and details per section (like ls -l)")
	layerFlag := flag.String("layer", "",
		"Run only these layers, comma-separated (e.g. infrastructure, workload, operator). "+
			"infrastructure=nodes,quotas; workload=deployments,pods,events,operator,dsci,dsc; operator=operator,dsci,dsc")
	sectionsFlag := flag.String("sections", "", "Comma-separated list of sections to run (e.g. nodes,quotas or deployments,pods). Overrides -layer.")

	// Configuration: flag default is env var (or static default).
	operatorNamespace := flag.String("operator-namespace", getEnvDefault(envOperatorNamespace, defaultOperatorNS),
		fmt.Sprintf("Namespace the operator is deployed to. Default: Env(%s) or %q", envOperatorNamespace, defaultOperatorNS))
	applicationsNamespace := flag.String("applications-namespace", getEnvDefault(envApplicationsNamespace, defaultAppsNS),
		fmt.Sprintf("Applications namespace (deployments, pods, events, quotas). Default: Env(%s) or %q", envApplicationsNamespace, defaultAppsNS))
	operatorDeployment := flag.String("operator-deployment", getEnvDefault(envOperatorDeployment, defaultOperatorDeploy),
		fmt.Sprintf("Operator deployment name. Default: Env(%s) or %q", envOperatorDeployment, defaultOperatorDeploy))
	monitoringNamespace := flag.String("monitoring-namespace", getEnvDefault(envMonitoringNamespace, defaultMonitoringNS),
		fmt.Sprintf("Monitoring namespace. Default: Env(%s) or %q", envMonitoringNamespace, defaultMonitoringNS))

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: health-check [flags]             run cluster health checks\n")
		fmt.Fprintf(os.Stderr, "       health-check validate manifests  validate manifest paths against manifests-config.yaml\n")
		fmt.Fprintf(os.Stderr, "\nCluster health flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nFor validate subcommand help: health-check validate manifests -help\n")
	}

	flag.Parse()

	if err := validateConfig(*operatorNamespace, *applicationsNamespace, *operatorDeployment, *monitoringNamespace); err != nil {
		fmt.Fprintf(os.Stderr, "health-check: %v\n", err)
		fmt.Fprintf(os.Stderr, "Set the listed env var or pass the corresponding flag. Run -help for details.\n")
		os.Exit(1)
	}

	cfg := loadConfig(*operatorNamespace, *applicationsNamespace, *operatorDeployment, *monitoringNamespace, *layerFlag, *sectionsFlag)

	kubeConfig, err := ctrl.GetConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "health-check: kube config: %v\n", err)
		os.Exit(1)
	}

	scheme := clientgoscheme.Scheme
	c, err := client.New(kubeConfig, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "health-check: client: %v\n", err)
		os.Exit(1)
	}
	cfg.Client = c

	report, err := clusterhealth.Run(context.Background(), cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "health-check: run: %v\n", err)
		os.Exit(1)
	}

	if *outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(os.Stderr, "health-check: json: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Printf("Cluster health check at %s\n\n", report.CollectedAt.Format("2006-01-02 15:04:05"))
		fmt.Print(report.PrettyPrint(*longFormat))
		fmt.Printf("\nOverall: %s\n", healthyStr(report.Healthy()))
	}

	if report.Healthy() {
		os.Exit(0)
	}
	os.Exit(1)
}

// runValidateCmd handles "health-check validate <subcommand>" dispatch.
func runValidateCmd(args []string) {
	if len(args) == 0 || args[0] != "manifests" {
		fmt.Fprintf(os.Stderr, "Usage: health-check validate manifests [-json] [-platform=odh|rhoai]\n")
		os.Exit(1)
	}

	fs := flag.NewFlagSet("validate manifests", flag.ExitOnError)
	outputJSON := fs.Bool("json", false, "Output as JSON")
	platformFlag := fs.String("platform", "odh", "Platform to validate: odh or rhoai")
	configPath := fs.String("config", "build/manifests-config.yaml", "Path to manifests-config.yaml")
	repoRoot := fs.String("repo-root", ".", "Path to operator repo root")

	if err := fs.Parse(args[1:]); err != nil {
		os.Exit(1)
	}

	platform := validate.PlatformODH
	if strings.EqualFold(*platformFlag, "rhoai") {
		platform = validate.PlatformRHOAI
	}

	summary, err := validate.ValidateManifests(validate.Config{
		ConfigPath: *configPath,
		RepoRoot:   *repoRoot,
		Platform:   platform,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "health-check validate: %v\n", err)
		os.Exit(1)
	}

	if *outputJSON {
		if err := validate.FormatJSON(os.Stdout, summary); err != nil {
			fmt.Fprintf(os.Stderr, "health-check validate: json: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Print(validate.FormatSummary(summary))
	}

	if summary.Healthy() {
		os.Exit(0)
	}
	os.Exit(1)
}

func healthyStr(healthy bool) string {
	if healthy {
		return "healthy"
	}
	return "unhealthy"
}

func loadConfig(operatorNS, appsNS, operatorDeploy, monitoringNS, layerFlag, sectionsFlag string) clusterhealth.Config {
	cfg := clusterhealth.Config{
		Operator: clusterhealth.OperatorConfig{
			Namespace: operatorNS,
			Name:      operatorDeploy,
		},
		Namespaces: clusterhealth.NamespaceConfig{
			Apps:       appsNS,
			Monitoring: monitoringNS,
			Extra:      []string{"kube-system"},
		},
		// DSCI and DSC are singletons; empty name means "discover the instance on the cluster".
		DSCI: types.NamespacedName{},
		DSC:  types.NamespacedName{},
	}

	if sectionsFlag != "" {
		cfg.OnlySections = strings.Split(strings.TrimSpace(sectionsFlag), ",")
		for i := range cfg.OnlySections {
			cfg.OnlySections[i] = strings.TrimSpace(cfg.OnlySections[i])
		}
	} else if layerFlag != "" {
		cfg.Layers = strings.Split(strings.TrimSpace(layerFlag), ",")
		for i := range cfg.Layers {
			cfg.Layers[i] = strings.TrimSpace(cfg.Layers[i])
		}
	}

	return cfg
}

// validateConfig returns an error if any required configuration is empty (e.g. env var not set and no flag).
func validateConfig(operatorNS, appsNS, operatorDeploy, monitoringNS string) error {
	var missing []string
	if strings.TrimSpace(operatorNS) == "" {
		missing = append(missing, fmt.Sprintf("operator-namespace (set %s or -operator-namespace)", envOperatorNamespace))
	}
	if strings.TrimSpace(appsNS) == "" {
		missing = append(missing, fmt.Sprintf("applications-namespace (set %s or -applications-namespace)", envApplicationsNamespace))
	}
	if strings.TrimSpace(operatorDeploy) == "" {
		missing = append(missing, fmt.Sprintf("operator-deployment (set %s or -operator-deployment)", envOperatorDeployment))
	}
	if strings.TrimSpace(monitoringNS) == "" {
		missing = append(missing, fmt.Sprintf("monitoring-namespace (set %s or -monitoring-namespace)", envMonitoringNamespace))
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("missing required configuration: %s", strings.Join(missing, "; "))
}

// getEnvDefault returns the env var value if set and non-empty (trimmed), otherwise fallback.
// Used to populate flag defaults so -help shows env-backed defaults.
func getEnvDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}
