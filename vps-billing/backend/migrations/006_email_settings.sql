CREATE TABLE IF NOT EXISTS email_settings (
  id TINYINT NOT NULL PRIMARY KEY,
  document JSON NOT NULL,
  CONSTRAINT email_settings_singleton CHECK (id = 1)
) ENGINE=InnoDB;
