-- The Sakila scripts create the "sakila" schema as root; let the compose user use it.
GRANT ALL ON sakila.* TO 'tessera'@'%';
