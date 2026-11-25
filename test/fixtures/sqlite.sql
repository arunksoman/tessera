-- SQLite has no exact decimal type: total keeps a float, so the conformance suite skips its
-- decimal check for this tile.
CREATE TABLE customers (
    id    INTEGER PRIMARY KEY,
    email VARCHAR(255) NOT NULL UNIQUE,
    name  VARCHAR(255)
);

CREATE TABLE orders (
    id          INTEGER PRIMARY KEY,
    customer_id BIGINT NOT NULL,
    total       DECIMAL(20, 4) NOT NULL,
    placed_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (customer_id) REFERENCES customers (id)
);

INSERT INTO customers (email, name) VALUES ('ada@example.com', 'Ada'), ('alan@example.com', NULL);
INSERT INTO orders (customer_id, total) VALUES (1, 12345678901234.5678);
