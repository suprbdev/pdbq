-- pdbcore fixture schema, shared by pdbq and pdbr (development stacks, e2e
-- suites, and testutil.FixtureCatalog which mirrors it).
-- Exercises: enums, arrays, jsonb, FKs, unique constraints, indexes, views,
-- functions, composites, PostGIS, RLS policies, and role switching.

CREATE TYPE mood AS ENUM ('sad', 'ok', 'happy');

CREATE TYPE address AS (
    street text,
    city   text,
    mood   mood
);

CREATE TABLE users (
    id         integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email      text NOT NULL UNIQUE,
    full_name  text,
    mood       mood,
    settings   jsonb,
    tags       text[],
    balance    bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    address        address,
    prev_addresses address[],
    moods          mood[]
);
CREATE INDEX users_mood_idx ON users (mood);
CREATE INDEX users_settings_idx ON users USING gin (settings);
CREATE INDEX users_tags_idx ON users USING gin (tags);
CREATE INDEX users_moods_idx ON users USING gin (moods);

CREATE TABLE posts (
    id         integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    author_id  integer NOT NULL REFERENCES users (id),
    title      text NOT NULL,
    body       text,
    published  boolean NOT NULL DEFAULT false
);
CREATE INDEX posts_author_id_idx ON posts (author_id);
CREATE INDEX posts_title_idx ON posts (title);

-- Self-referential reply tree (Reddit-style threads). parent_id is the
-- recursive edge; @costMultiplier tells the cost estimator to assume a small
-- average fan-out per level instead of the requested page size, which would
-- otherwise grow as page^depth and reject legitimate thread queries.
CREATE TABLE comments (
    id         integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    post_id    integer NOT NULL REFERENCES posts (id),
    parent_id  integer REFERENCES comments (id),
    body       text NOT NULL
);
CREATE INDEX comments_post_id_idx ON comments (post_id);
CREATE INDEX comments_parent_id_idx ON comments (parent_id);

COMMENT ON CONSTRAINT comments_parent_id_fkey ON comments IS '@costMultiplier 3';

-- @enum table: its rows become the values of a generated GraphQL enum
-- (EventType), the table itself disappears from the API, and columns
-- referencing it via FK are typed as the enum. The description column
-- becomes per-value descriptions.
CREATE TABLE event_type (
    code        text PRIMARY KEY,
    description text
);
COMMENT ON TABLE event_type IS E'@enum\nKind of event.';

INSERT INTO event_type (code, description) VALUES
    ('conference', 'A large formal gathering'),
    ('hackathon',  NULL),
    ('meetup',     'Casual get-together');

CREATE TABLE events (
    id         integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       text NOT NULL,
    type_code  text NOT NULL REFERENCES event_type (code)
);
CREATE INDEX events_type_code_idx ON events (type_code);

INSERT INTO events (name, type_code) VALUES
    ('PGConf',         'conference'),
    ('Hack Night',     'hackathon'),
    ('GraphQL Meetup', 'meetup');

-- PostGIS spatial fixture. The extension ships in the compose/CI image; the
-- table is only created when it is actually available so this schema still
-- loads against a plain postgres image.
CREATE EXTENSION IF NOT EXISTS postgis;

CREATE TABLE places (
    id        integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- The FK gives nested mutations a spatial target (createUser with nested
    -- places), which is where geometry values flow through the plugin's own
    -- CTE builder rather than the core compiler.
    owner_id  integer REFERENCES users (id),
    name      text NOT NULL,
    -- geometry: planar, SRID units. geography: spheroidal, metres.
    location  geometry(Point, 4326),
    area      geography(Polygon, 4326)
);
CREATE INDEX places_owner_id_idx ON places (owner_id);
CREATE INDEX places_location_idx ON places USING gist (location);
CREATE INDEX places_area_idx ON places USING gist (area);

-- PK-less view: no node identity, offset pagination only.
CREATE VIEW metrics AS
    SELECT 'users'::text AS name, count(*)::integer AS value FROM users
    UNION ALL
    SELECT 'posts'::text, count(*)::integer FROM posts;

CREATE FUNCTION search_posts(term text) RETURNS SETOF posts
STABLE LANGUAGE sql AS $$
    SELECT * FROM posts WHERE title ILIKE '%' || term || '%';
$$;

-- Computed columns: stable functions whose first argument is a row type
-- become fields on that type (users_post_count -> User.postCount).
CREATE FUNCTION users_post_count(u users) RETURNS bigint
STABLE LANGUAGE sql AS $$
    SELECT count(*) FROM posts WHERE author_id = u.id;
$$;

-- Extra scalar arguments become GraphQL field arguments (Post.excerpt(maxChars:)).
CREATE FUNCTION posts_excerpt(p posts, max_chars integer) RETURNS text
STABLE LANGUAGE sql AS $$
    SELECT left(coalesce(p.body, ''), coalesce(max_chars, 80));
$$;

-- Set-returning computed columns become list fields (User.recentPosts(n:),
-- User.tagWords).
CREATE FUNCTION users_recent_posts(u users, n integer) RETURNS SETOF posts
STABLE LANGUAGE sql AS $$
    SELECT * FROM posts WHERE author_id = u.id ORDER BY id DESC LIMIT coalesce(n, 5);
$$;

CREATE FUNCTION users_tag_words(u users) RETURNS SETOF text
STABLE LANGUAGE sql AS $$
    SELECT unnest(u.tags);
$$;

-- Volatile functions become Relay-classic mutations:
-- fn(input: FnInput!): FnPayload! { result clientMutationId }.
CREATE FUNCTION add_numbers(a integer, b integer) RETURNS integer
VOLATILE LANGUAGE sql AS $$
    SELECT a + b;
$$;

CREATE FUNCTION publish_post(post_id integer) RETURNS posts
VOLATILE LANGUAGE sql AS $$
    UPDATE posts SET published = true WHERE id = post_id RETURNING *;
$$;

CREATE FUNCTION unpublish_post(post_id integer) RETURNS posts
VOLATILE LANGUAGE sql AS $$
    UPDATE posts SET published = false WHERE id = post_id RETURNING *;
$$;

-- Array-typed function arguments become GraphQL lists.
CREATE FUNCTION word_lengths(words text[]) RETURNS integer
VOLATILE LANGUAGE sql AS $$
    SELECT coalesce(array_length(words, 1), 0);
$$;

-- Enum-typed function returns/arguments map to the generated enum type, so
-- both directions speak GraphQL enum value names rather than raw pg labels.
CREATE FUNCTION best_mood() RETURNS mood
STABLE LANGUAGE sql AS $$
    SELECT 'happy'::mood;
$$;

CREATE FUNCTION all_moods() RETURNS mood[]
STABLE LANGUAGE sql AS $$
    SELECT ARRAY['sad', 'ok']::mood[];
$$;

CREATE FUNCTION mood_moods() RETURNS SETOF mood
STABLE LANGUAGE sql AS $$
    SELECT unnest(ARRAY['ok', 'happy']::mood[]);
$$;

CREATE FUNCTION describe_mood(m mood) RETURNS text
STABLE LANGUAGE sql AS $$
    SELECT 'mood is ' || m::text;
$$;

-- Volatile enum return: exercises the mutation payload path.
CREATE FUNCTION bump_mood(m mood) RETURNS mood
VOLATILE LANGUAGE sql AS $$
    SELECT CASE m WHEN 'sad' THEN 'ok' WHEN 'ok' THEN 'happy' ELSE 'happy' END::mood;
$$;

-- JWT minting (rls.auth.jwt_type = public.jwt): functions returning this
-- composite yield signed tokens; the fields become claims.
CREATE TYPE jwt AS (
    exp     bigint,
    user_id integer,
    role    text
);

CREATE FUNCTION authenticate(user_email text) RETURNS jwt
VOLATILE LANGUAGE sql AS $$
    SELECT (extract(epoch FROM now())::bigint + 3600, id, 'app_user')::jwt
    FROM users WHERE email = user_email;
$$;

-- Deterministic serialization-failure probe (exercises transactions.max_retries
-- in the e2e suite): odd sequence values raise 40001, and sequence advances
-- survive the rollback, so the retry deterministically succeeds.
CREATE SEQUENCE retry_probe_seq;
CREATE FUNCTION retry_probe() RETURNS integer
VOLATILE LANGUAGE plpgsql AS $$
DECLARE n bigint;
BEGIN
    n := nextval('retry_probe_seq');
    IF n % 2 = 1 THEN
        RAISE EXCEPTION 'synthetic serialization failure' USING ERRCODE = '40001';
    END IF;
    RETURN n::integer;
END $$;

-- Roles for the RLS demo. The anonymous role only sees published posts;
-- app_user additionally sees their own rows (claim pdbq.claims.user_id).
DO $$ BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'anonymous') THEN
        CREATE ROLE anonymous NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_user') THEN
        CREATE ROLE app_user NOLOGIN;
    END IF;
END $$;

GRANT USAGE ON SCHEMA public TO anonymous, app_user;
GRANT SELECT ON users, posts, places, metrics TO anonymous;
GRANT SELECT, INSERT, UPDATE, DELETE ON users, posts, places TO app_user;
GRANT SELECT ON metrics TO app_user;
GRANT EXECUTE ON FUNCTION search_posts(text), users_post_count(users), posts_excerpt(posts, integer),
    users_recent_posts(users, integer), users_tag_words(users), retry_probe(),
    add_numbers(integer, integer), publish_post(integer), unpublish_post(integer),
    word_lengths(text[]), authenticate(text) TO anonymous, app_user;
GRANT USAGE ON SEQUENCE retry_probe_seq TO anonymous, app_user;

ALTER TABLE posts ENABLE ROW LEVEL SECURITY;
CREATE POLICY posts_public ON posts FOR SELECT
    USING (published OR author_id = NULLIF(current_setting('pdbq.claims.user_id', true), '')::integer);
CREATE POLICY posts_own_writes ON posts FOR ALL TO app_user
    USING (author_id = NULLIF(current_setting('pdbq.claims.user_id', true), '')::integer)
    WITH CHECK (author_id = NULLIF(current_setting('pdbq.claims.user_id', true), '')::integer);

-- Seed data.
INSERT INTO users (email, full_name, mood, settings, tags, address, prev_addresses, moods) VALUES
    ('ada@example.com',   'Ada Lovelace',  'happy', '{"theme": "dark"}',  ARRAY['admin', 'founder'],
        ROW('12 St James Sq', 'London', 'happy')::address,
        ARRAY[ROW('1 Ockham Park', 'Surrey', 'ok')::address],
        ARRAY['sad', 'happy']::mood[]),
    ('grace@example.com', 'Grace Hopper',  'ok',    '{"theme": "light"}', ARRAY['staff'], NULL, NULL,
        ARRAY['ok']::mood[]),
    ('alan@example.com',  'Alan Turing',   'happy', NULL,                 NULL, NULL, NULL, NULL);

-- Spatial seed: three London-ish points plus one far away, so bounding-box
-- and radius queries have both hits and misses.
INSERT INTO places (owner_id, name, location, area) VALUES
    (1, 'Trafalgar Square', ST_SetSRID(ST_MakePoint(-0.1281, 51.5080), 4326),
        ST_GeogFromText('SRID=4326;POLYGON((-0.130 51.507, -0.126 51.507, -0.126 51.509, -0.130 51.509, -0.130 51.507))')),
    (1, 'British Museum',   ST_SetSRID(ST_MakePoint(-0.1270, 51.5194), 4326), NULL),
    (2, 'Greenwich',        ST_SetSRID(ST_MakePoint(-0.0005, 51.4779), 4326), NULL),
    (3, 'Sydney Opera House', ST_SetSRID(ST_MakePoint(151.2153, -33.8568), 4326), NULL);

INSERT INTO posts (author_id, title, body, published) VALUES
    (1, 'Notes on the Analytical Engine', 'First!',            true),
    (1, 'Unpublished draft',              'Secret.',           false),
    (2, 'Compilers 101',                  'COBOL forever.',    true),
    (3, 'On Computable Numbers',          'Halting problems.', true);
