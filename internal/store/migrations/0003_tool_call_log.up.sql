CREATE TABLE IF NOT EXISTS tool_call_log (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    task_id BIGINT UNSIGNED NOT NULL,
    tool_name VARCHAR(128) NOT NULL,
    input_json JSON NOT NULL,
    output_json JSON NOT NULL,
    status VARCHAR(32) NOT NULL,
    error TEXT NULL,
    duration_ms BIGINT UNSIGNED NOT NULL DEFAULT 0,
    created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    KEY idx_tool_call_log_task (task_id, id),
    KEY idx_tool_call_log_tool (tool_name, created_at),
    CONSTRAINT fk_tool_call_log_task
        FOREIGN KEY (task_id) REFERENCES review_task (id)
        ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
