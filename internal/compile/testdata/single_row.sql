-- field: userById
SELECT jsonb_build_object('id', __t_1."id", 'email', __t_1."email", 'fullName', __t_1."full_name", 'createdAt', __t_1."created_at") AS data
FROM "public"."users" AS __t_1
WHERE __t_1."id" = $1
LIMIT 1
-- args: [1]

