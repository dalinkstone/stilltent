-- stilltent: TiDB initialization for mnemo-server (mem9)
-- Run once after TiDB is healthy: mysql -h 127.0.0.1 -P 4000 -u root < scripts/init-tidb.sql
--
-- The VECTOR(256) column is REQUIRED — embedding is core functionality.
-- embed-service (local C binary) generates 256-dim L2-normalized vectors.
-- The HNSW index is optional (requires TiFlash); without it, vector search
-- uses brute-force scan which is still functional for typical workloads.

CREATE DATABASE IF NOT EXISTS mnemos;
CREATE DATABASE IF NOT EXISTS mnemos_tenant;

-- Control plane tables (mnemos)
USE mnemos;

CREATE TABLE IF NOT EXISTS tenants (
  id              VARCHAR(36)   PRIMARY KEY,
  name            VARCHAR(255)  NOT NULL,
  db_host         VARCHAR(255)  NOT NULL,
  db_port         INT           NOT NULL,
  db_user         VARCHAR(255)  NOT NULL,
  db_password     VARCHAR(255)  NOT NULL,
  db_name         VARCHAR(255)  NOT NULL,
  db_tls          TINYINT(1)    NOT NULL DEFAULT 0,
  provider        VARCHAR(50)   NOT NULL,
  cluster_id      VARCHAR(255)  NULL,
  claim_url       TEXT          NULL,
  claim_expires_at TIMESTAMP    NULL,
  status          VARCHAR(20)   NOT NULL DEFAULT 'provisioning'
                  COMMENT 'provisioning|active|suspended|deleted',
  schema_version  INT           NOT NULL DEFAULT 1,
  created_at      TIMESTAMP     DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP     DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  deleted_at      TIMESTAMP     NULL,
  UNIQUE INDEX idx_tenant_name (name),
  INDEX idx_tenant_status (status),
  INDEX idx_tenant_provider (provider)
);

CREATE TABLE IF NOT EXISTS upload_tasks (
  task_id       VARCHAR(36)   PRIMARY KEY,
  tenant_id     VARCHAR(36)   NOT NULL,
  file_name     VARCHAR(255)  NOT NULL,
  file_path     TEXT          NOT NULL,
  agent_id      VARCHAR(100)  NULL,
  session_id    VARCHAR(100)  NULL,
  file_type     VARCHAR(20)   NOT NULL COMMENT 'session|memory',
  total_chunks  INT           NOT NULL DEFAULT 0,
  done_chunks   INT           NOT NULL DEFAULT 0,
  status        VARCHAR(20)   NOT NULL DEFAULT 'pending'
                COMMENT 'pending|processing|done|failed',
  error_msg     TEXT          NULL,
  created_at    TIMESTAMP     DEFAULT CURRENT_TIMESTAMP,
  updated_at    TIMESTAMP     DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_upload_tenant (tenant_id),
  INDEX idx_upload_poll (status, created_at)
);

-- Seed local-dev tenant (API key = tenant ID)
INSERT INTO tenants (id, name, db_host, db_port, db_user, db_password, db_name, db_tls, provider, status)
VALUES ('stilltent-local-dev-key', 'Local Dev', 'tidb', 4000, 'root', '', 'mnemos_tenant', 0, 'starter', 'active')
ON DUPLICATE KEY UPDATE status = 'active';

-- Data plane tables (mnemos_tenant)
USE mnemos_tenant;

CREATE TABLE IF NOT EXISTS memories (
  id              VARCHAR(36)     PRIMARY KEY,
  content         MEDIUMTEXT      NOT NULL,
  source          VARCHAR(100),
  tags            JSON,
  metadata        JSON,
  embedding       VECTOR(256)     NULL,
  memory_type     VARCHAR(20)     NOT NULL DEFAULT 'pinned'
                  COMMENT 'pinned|insight|digest',
  agent_id        VARCHAR(100)    NULL     COMMENT 'Agent that created this memory',
  session_id      VARCHAR(100)    NULL     COMMENT 'Session this memory originated from',
  state           VARCHAR(20)     NOT NULL DEFAULT 'active'
                  COMMENT 'active|paused|archived|deleted',
  version         INT             DEFAULT 1,
  updated_by      VARCHAR(100),
  created_at      TIMESTAMP       DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP       DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  superseded_by   VARCHAR(36)     NULL     COMMENT 'ID of the memory that replaced this one',
  INDEX idx_memory_type         (memory_type),
  INDEX idx_source              (source),
  INDEX idx_state               (state),
  INDEX idx_agent               (agent_id),
  INDEX idx_session             (session_id),
  INDEX idx_updated             (updated_at)
);

-- Migrate embedding column to 256 dims if it exists with a different size.
-- This is safe: ALTER COLUMN on VECTOR type re-creates the column.
-- Existing embeddings (if any) from the old 768/1536-dim model are incompatible
-- and will be cleared (set to NULL) so the new embed-service can re-generate them.
UPDATE memories SET embedding = NULL WHERE embedding IS NOT NULL;
ALTER TABLE memories MODIFY COLUMN embedding VECTOR(256) NULL;

-- ---------------------------------------------------------------------------
-- Composite index for filtered vector search.
-- VectorSearch always filters on state (usually 'active') and
-- embedding IS NOT NULL. TiDB does not support partial indexes, so we
-- create a composite B-tree index covering the most common filter columns.
-- This lets the optimizer narrow rows before the ANN scan.
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_state_agent_session ON memories (state, agent_id, session_id);

-- Verify
SHOW DATABASES;
