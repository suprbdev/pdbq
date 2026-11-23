-- field: updateUserByEmail
WITH __mut AS (
  UPDATE "public"."users"
  SET "full_name" = $1
  WHERE "public"."users"."email" = $2
  RETURNING "id", "email", "full_name"
)
SELECT json_build_object('user', json_build_object('id', __t_1."id", 'email', __t_1."email", 'fullName', __t_1."full_name")) AS data
FROM __mut AS __t_1
-- args: ["Grace","a@b.c"]

-- field: deleteUserByEmail
WITH __mut AS (
  DELETE FROM "public"."users"
  WHERE "public"."users"."email" = $1
  RETURNING "id"
)
SELECT json_build_object('user', json_build_object('id', __t_1."id"), 'clientMutationId', NULL::text) AS data
FROM __mut AS __t_1
-- args: ["b@c.d"]

