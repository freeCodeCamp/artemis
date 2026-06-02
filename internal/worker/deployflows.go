package worker

const (
	WorkflowFinalize = "finalize"
	WorkflowPromote  = "promote"
	WorkflowRollback = "rollback"
)

func RegisterDeployWorkflows(rt *Runtime, finalize, promote, rollback Handler) error {
	defs := []WorkflowDef{
		{Name: WorkflowFinalize, ConcurrencyKey: ConcurrencyKeySite, Handler: finalize},
		{Name: WorkflowPromote, ConcurrencyKey: ConcurrencyKeySite, Handler: promote},
		{Name: WorkflowRollback, ConcurrencyKey: ConcurrencyKeySite, Handler: rollback},
	}
	for _, d := range defs {
		if err := rt.Register(d); err != nil {
			return err
		}
	}
	return nil
}
