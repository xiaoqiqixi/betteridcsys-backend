ALTER TABLE users ADD COLUMN disabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE users ADD COLUMN revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE users ADD CONSTRAINT users_disabled_boolean CHECK(disabled IN (0,1));
ALTER TABLE users ADD CONSTRAINT users_revision_min CHECK(revision >= 1);
ALTER TABLE users ADD CONSTRAINT users_revision_max CHECK(revision <= 9007199254740991);
