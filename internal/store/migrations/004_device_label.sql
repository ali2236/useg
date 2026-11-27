-- Admin-set display name; mDNS advertising keeps writing `name`, which can be
-- missing or useless, so this override wins in every label.
ALTER TABLE device ADD COLUMN label TEXT;
