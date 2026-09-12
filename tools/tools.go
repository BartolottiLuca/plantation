// Package tools pins C01's allowed dependencies so go mod tidy cannot drop
// them before C02 and C04 import them.
package tools

import (
	_ "github.com/jackc/pgx/v5"
	_ "gopkg.in/yaml.v3"
)
