package eventmessage

import (
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/so/ent/predicate"
)

// GreaterThanCursor filters rows with (create_time, id) tuple greater than the provided cursor.
func GreaterThanCursor(createTime time.Time, id uuid.UUID) predicate.EventMessage {
	return predicate.EventMessage(func(s *sql.Selector) {
		s.Where(
			sql.Or(
				sql.GT(s.C(FieldCreateTime), createTime),
				sql.And(
					sql.EQ(s.C(FieldCreateTime), createTime),
					sql.GT(s.C(FieldID), id),
				),
			),
		)
	})
}
