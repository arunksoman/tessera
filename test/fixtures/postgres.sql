CREATE TABLE customers (
    id    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email text NOT NULL UNIQUE,
    name  text
);

CREATE TABLE orders (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    customer_id bigint NOT NULL REFERENCES customers (id),
    total       numeric(20, 4) NOT NULL,
    placed_at   timestamptz NOT NULL DEFAULT now()
);

INSERT INTO customers (email, name) VALUES ('ada@example.com', 'Ada'), ('alan@example.com', NULL);
INSERT INTO orders (customer_id, total) VALUES (1, 12345678901234.5678);
