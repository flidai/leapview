-- SQLC-only projection of River's upstream operational table.
-- This file is never embedded or applied at runtime; River migrations own the
-- actual public.river_job table.

CREATE TYPE public.river_job_state AS ENUM (
    'available',
    'cancelled',
    'completed',
    'discarded',
    'pending',
    'retryable',
    'running',
    'scheduled'
);

CREATE TABLE public.river_job (
    id           bigint NOT NULL,
    state        public.river_job_state NOT NULL,
    attempt      smallint NOT NULL,
    attempted_by text[]
);
