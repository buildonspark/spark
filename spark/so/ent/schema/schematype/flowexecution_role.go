package schematype

type FlowExecutionRole string

const (
	FlowExecutionRoleCoordinator FlowExecutionRole = "COORDINATOR"
	FlowExecutionRoleParticipant FlowExecutionRole = "PARTICIPANT"
)

func (FlowExecutionRole) Values() []string {
	return []string{
		string(FlowExecutionRoleCoordinator),
		string(FlowExecutionRoleParticipant),
	}
}
