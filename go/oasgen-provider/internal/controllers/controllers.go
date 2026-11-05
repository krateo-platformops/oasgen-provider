package controllers

import (
	"github.com/krateo-platformops/provider-runtime/pkg/controller"
	"github.com/krateo-platformops/provider-runtime/pkg/reconciler"
	ctrl "sigs.k8s.io/controller-runtime"

	repo "github.com/krateo-platformops/oasgen-provider/internal/controllers/restdefinition"
)

// Setup creates all controllers with the supplied logger and adds them to
// the supplied manager. metrics records the provider_runtime.reconcile.*
// telemetry suite and is a working no-op (nil) when OTEL_ENABLED is false.
// Config re-exports the controller's process-level generator settings so main configures them without
// importing the controller package directly. An alias rather than a wrapper struct: a second type would
// have to be kept in sync field by field, which is the kind of duplication that silently drops a field.
type Config = repo.GeneratorConfig

func Setup(mgr ctrl.Manager, o controller.Options, metrics reconciler.MetricsRecorder, cfg Config) error {
	for _, setup := range []func(ctrl.Manager, controller.Options, reconciler.MetricsRecorder, Config) error{
		repo.Setup,
	} {
		if err := setup(mgr, o, metrics, cfg); err != nil {
			return err
		}
	}
	return nil
}
