-- pdbq development / integration fixture schema.
-- Exercises: enums, arrays, jsonb, FKs, unique constraints, indexes,
-- functions, RLS policies, and role switching.

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
    prev_addresses address[]
);
CREATE INDEX users_mood_idx ON users (mood);
CREATE INDEX users_settings_idx ON users USING gin (settings);
CREATE INDEX users_tags_idx ON users USING gin (tags);

CREATE TABLE posts (
    id         integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    author_id  integer NOT NULL REFERENCES users (id),
    title      text NOT NULL,
    body       text,
    published  boolean NOT NULL DEFAULT false
);
CREATE INDEX posts_author_id_idx ON posts (author_id);
CREATE INDEX posts_title_idx ON posts (title);

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
GRANT SELECT ON users, posts TO anonymous;
GRANT SELECT, INSERT, UPDATE, DELETE ON users, posts TO app_user;
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
INSERT INTO users (email, full_name, mood, settings, tags, address, prev_addresses) VALUES
    ('ada@example.com',   'Ada Lovelace',  'happy', '{"theme": "dark"}',  ARRAY['admin', 'founder'],
        ROW('12 St James Sq', 'London', 'happy')::address,
        ARRAY[ROW('1 Ockham Park', 'Surrey', 'ok')::address]),
    ('grace@example.com', 'Grace Hopper',  'ok',    '{"theme": "light"}', ARRAY['staff'], NULL, NULL),
    ('alan@example.com',  'Alan Turing',   'happy', NULL,                 NULL, NULL, NULL);

INSERT INTO posts (author_id, title, body, published) VALUES
    (1, 'Notes on the Analytical Engine', 'First!',            true),
    (1, 'Unpublished draft',              'Secret.',           false),
    (2, 'Compilers 101',                  'COBOL forever.',    true),
    (3, 'On Computable Numbers',          'Halting problems.', true);
