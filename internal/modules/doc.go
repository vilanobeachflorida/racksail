// Package modules is the module manager: manifests, registration, profiles,
// dependencies, hardware checks and the on, active and off lifecycle.
//
// Every bundled module lives in modules/<id> and registers itself from an
// init function:
//
//	//go:embed manifest.yaml
//	var manifest []byte
//
//	type Module struct{ modules.Base }
//
//	func init() { modules.Register(&Module{Base: modules.NewBase(manifest)}) }
//
// A Module only has to provide its manifest. Everything else is optional and
// discovered through interfaces: KindProvider for kinds, ReconcilerProvider,
// APIProvider for API operations beyond the generated ones, HookProvider,
// EventProvider, MigrationProvider, RecipeProvider, BackupProvider,
// HealthProvider, HintProvider for the assistant, OwnershipProvider for the
// module that keeps resellers, and Starter and Stopper for work that runs
// while the module is on. Each capability receives the
// Services it needs.
//
// Modules are compiled into the binary and stay dormant until turned on;
// even then they install nothing until a resource needs them.
package modules
