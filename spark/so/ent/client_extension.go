package ent

import (
	"database/sql"
	"fmt"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

// RawDB returns the underlying *sql.DB from the Ent client driver.
func (c *Client) RawDB() (*sql.DB, error) {
	return rawDBFromDriver(c.driver)
}

func rawDBFromDriver(driver dialect.Driver) (*sql.DB, error) {
	switch d := driver.(type) {
	case *entsql.Driver:
		return d.DB(), nil
	case *dialect.DebugDriver:
		return rawDBFromDriver(d.Driver)
	default:
		return nil, fmt.Errorf("unsupported ent driver type for raw DB access: %T", driver)
	}
}
