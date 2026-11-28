-- DuckDB has no autoincrement column, so the fixture gives its ids.
CREATE TABLE customers (
    id    INTEGER PRIMARY KEY,
    email VARCHAR(255) NOT NULL UNIQUE,
    name  VARCHAR(255)
);

CREATE TABLE orders (
    id          INTEGER PRIMARY KEY,
    customer_id INTEGER NOT NULL,
    total       DECIMAL(20, 4) NOT NULL,
    placed_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (customer_id) REFERENCES customers (id)
);

INSERT INTO customers (id, email, name) VALUES (1, 'ada@example.com', 'Ada'), (2, 'alan@example.com', NULL);
INSERT INTO orders (id, customer_id, total) VALUES (1, 1, 12345678901234.5678);
