-- field: createUser
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "full_name", "mood")
  VALUES ($1, $2, $3)
  RETURNING *
)
SELECT jsonb_build_object('user', __row.data, 'clientMutationId', $4::text) AS data
FROM (
  SELECT jsonb_build_object('nodeId', replace(encode(convert_to(jsonb_build_array('User'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', ''), 'id', __t_1."id", 'email', __t_1."email", 'mood', CASE __t_1."mood" WHEN 'happy' THEN 'HAPPY' WHEN 'ok' THEN 'OK' WHEN 'sad' THEN 'SAD' END) AS data
  FROM __mut AS __t_1
) AS __row
-- args: ["a@b.c","Ada","happy","cm-1"]

-- field: updateUserById
WITH __mut AS (
  UPDATE "public"."users"
  SET "full_name" = $1
  WHERE "public"."users"."id" = $2
  RETURNING *
)
SELECT jsonb_build_object('user', __row.data, 'clientMutationId', $3::text) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'fullName', __t_1."full_name") AS data
  FROM __mut AS __t_1
) AS __row
-- args: ["Grace",1,"cm-2"]

-- field: deleteUserById
WITH __mut AS (
  DELETE FROM "public"."users"
  WHERE "public"."users"."id" = $1
  RETURNING *
)
SELECT jsonb_build_object('user', __row.data, 'clientMutationId', $2::text) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS data
  FROM __mut AS __t_1
) AS __row
-- args: [2,"cm-3"]

