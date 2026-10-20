SET LOCAL lock_timeout = '3s';
SET LOCAL statement_timeout = '30s';

ALTER TABLE stores
    ADD COLUMN guest_display_name text NULL
    CONSTRAINT stores_guest_display_name_check CHECK (
        guest_display_name IS NULL
        OR (
            guest_display_name = btrim(guest_display_name)
            AND char_length(guest_display_name) BETWEEN 1 AND 80
            AND guest_display_name !~ '[[:cntrl:]]'
        )
    );

SET LOCAL lock_timeout = DEFAULT;
SET LOCAL statement_timeout = DEFAULT;
