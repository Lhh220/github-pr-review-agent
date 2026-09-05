CREATE TABLE IF NOT EXISTS review_task (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    repo VARCHAR(255) NOT NULL,
    pr_number INT UNSIGNED NOT NULL,
    commit_sha VARCHAR(40) NOT NULL,
    action VARCHAR(32) NOT NULL,
    delivery_id VARCHAR(64) NOT NULL,
    status VARCHAR(32) NOT NULL,
    error TEXT NULL,
    attempt_count INT UNSIGNED NOT NULL DEFAULT 0,
    max_attempts INT UNSIGNED NOT NULL DEFAULT 3,
    next_retry_at TIMESTAMP(3) NULL,
    created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE KEY uq_review_task_delivery (delivery_id),
    KEY idx_review_task_repo_pr (repo, pr_number),
    KEY idx_review_task_status (status),
    KEY idx_review_task_retry (status, next_retry_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS review_result (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    task_id BIGINT UNSIGNED NOT NULL,
    summary VARCHAR(2048) NOT NULL,
    payload_json JSON NOT NULL,
    raw_response MEDIUMTEXT NOT NULL,
    model VARCHAR(128) NOT NULL,
    input_tokens INT UNSIGNED NOT NULL DEFAULT 0,
    output_tokens INT UNSIGNED NOT NULL DEFAULT 0,
    total_tokens INT UNSIGNED NOT NULL DEFAULT 0,
    llm_duration_ms BIGINT UNSIGNED NOT NULL DEFAULT 0,
    created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    UNIQUE KEY uq_review_result_task (task_id),
    CONSTRAINT fk_review_result_task
        FOREIGN KEY (task_id) REFERENCES review_task (id)
        ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
