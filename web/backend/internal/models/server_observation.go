package models

import (
	"context"
	"database/sql"
	"github.com/google/uuid"
)

func UpdateServerSystemInfoContext(ctx context.Context, db *sql.DB, id uuid.UUID, cores int, memory, disk int64) error {
	_, err := db.ExecContext(ctx, `UPDATE servers SET cpu_cores=$1,memory_total_bytes=$2,disk_total_bytes=$3 WHERE id=$4`, cores, memory, disk, id)
	return err
}

func UpdateDockerInfoContext(ctx context.Context, db *sql.DB, id uuid.UUID, installed bool, version string) error {
	_, err := db.ExecContext(ctx, `UPDATE servers SET has_docker=$1,docker_version=$2 WHERE id=$3`, installed, version, id)
	return err
}
