-- +goose Up
-- Nullable, free-text sub-stage within a job_type -- not a fixed enum,
-- since which phases exist (if any beyond the job_type's own default) is
-- entirely up to each handler. Most job types map 1:1 to a single phase
-- derived from job_type alone and never write this column at all;
-- region_classification is the first job type that needs an explicit
-- transition (region analysis -> embedding) since both happen inside one
-- job without an intervening jobs-table update. See internal/queue's
-- phase registry for the display-facing label/message per phase.
ALTER TABLE jobs ADD COLUMN phase TEXT;

-- +goose Down
ALTER TABLE jobs DROP COLUMN phase;
