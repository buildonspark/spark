package schematype

type FlowExecutionStatus string

const (
	FlowExecutionStatusInFlight   FlowExecutionStatus = "IN_FLIGHT"
	FlowExecutionStatusCommitted  FlowExecutionStatus = "COMMITTED"
	FlowExecutionStatusRolledBack FlowExecutionStatus = "ROLLED_BACK"
)

func (FlowExecutionStatus) Values() []string {
	return []string{
		string(FlowExecutionStatusInFlight),
		string(FlowExecutionStatusCommitted),
		string(FlowExecutionStatusRolledBack),
	}
}
