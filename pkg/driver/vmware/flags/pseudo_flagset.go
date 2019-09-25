package flags

import (
	"context"
	"github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	"github.com/vmware/govmomi"
)

type pseudoFlagKey string
type flagKey string

var (
	clientPseudoFlagKey = pseudoFlagKey("client")
	specPseudoFlagKey   = pseudoFlagKey("spec")
)

func ContextWithPseudoFlagset(ctx context.Context, client *govmomi.Client, spec *v1alpha1.VMwareMachineClassSpec) context.Context {
	ctx = context.WithValue(ctx, clientPseudoFlagKey, client)
	ctx = context.WithValue(ctx, specPseudoFlagKey, spec)
	return ctx
}

func GetClientFromPseudoFlagset(ctx context.Context) *govmomi.Client {
	return ctx.Value(clientPseudoFlagKey).(*govmomi.Client)
}

func GetSpecFromPseudoFlagset(ctx context.Context) *v1alpha1.VMwareMachineClassSpec {
	return ctx.Value(specPseudoFlagKey).(*v1alpha1.VMwareMachineClassSpec)
}
