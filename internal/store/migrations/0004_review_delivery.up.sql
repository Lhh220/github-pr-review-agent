CREATE TABLE IF NOT EXISTS review_delivery (
    task_id BIGINT UNSIGNED PRIMARY KEY,
    commit_sha VARCHAR(40) NOT NULL,
    marker CHAR(32) NOT NULL,
    github_review_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE KEY uq_review_delivery_marker (marker),
    CONSTRAINT fk_review_delivery_task FOREIGN KEY (task_id) REFERENCES review_task(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
