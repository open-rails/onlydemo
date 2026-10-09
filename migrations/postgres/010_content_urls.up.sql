-- parent: 9 sha256:47081cb6da2af5699077a85fe40eb6888cd16ad955dd44aa60050c3bdf230895
-- Posts are /p/{CODE}/{slug}: ContentKit's content_codes holds each post's
-- code and its slug from the title. Public images have fixed names in
-- ContentKit's media file system, so nothing records slots.
ALTER TABLE posts DROP COLUMN slug;
DROP TABLE media_slots;
