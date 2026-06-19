-- Add the `source` column to notifications for existing installs that
-- ran migration 040 before the Source/Kind split. Source is the filter
-- taxonomy axis the Signal tray uses (mesh / approval / system /
-- secret); Kind is the producer's sub-classification. They're
-- orthogonal: Source = who produced it, Kind = what it is.
--
-- Backfill existing rows to 'mesh' — historically the only source that
-- fired notify events.

ALTER TABLE notifications ADD COLUMN source TEXT NOT NULL DEFAULT 'mesh';

CREATE INDEX IF NOT EXISTS notifications_source_idx ON notifications(source);
