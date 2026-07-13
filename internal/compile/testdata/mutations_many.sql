-- field: createUsers
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "full_name", "mood")
  VALUES ($1, $2, $3),
         ($4, DEFAULT, DEFAULT)
  RETURNING *
)
SELECT jsonb_build_object('users', (SELECT coalesce(jsonb_agg(__s_2.data), '[]'::jsonb) FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'email', __t_1."email") AS data
  FROM __mut AS __t_1
) AS __s_2), 'affectedCount', (SELECT count(*) FROM __mut), 'clientMutationId', $5::text) AS data
-- args: ["a@b.c","Ada","happy","b@c.d","cm-many"]

-- field: updateUsers
WITH __mut AS (
  UPDATE "public"."users"
  SET "mood" = $1
  WHERE "public"."users"."mood" = $2
  RETURNING *
)
SELECT jsonb_build_object('users', (SELECT coalesce(jsonb_agg(__s_2.data), '[]'::jsonb) FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'mood', CASE __t_1."mood" WHEN 'happy' THEN 'HAPPY' WHEN 'ok' THEN 'OK' WHEN 'sad' THEN 'SAD' END) AS data
  FROM __mut AS __t_1
) AS __s_2), 'affectedCount', (SELECT count(*) FROM __mut)) AS data
-- args: ["ok","sad"]

-- field: deleteUsers
WITH __mut AS (
  DELETE FROM "public"."users"
  WHERE "public"."users"."email" LIKE $1
  RETURNING *
)
SELECT jsonb_build_object('affectedCount', (SELECT count(*) FROM __mut)) AS data
-- args: ["%@spam.example"]

