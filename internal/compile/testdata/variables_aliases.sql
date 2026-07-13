-- field: userById
SELECT jsonb_build_object('theEmail', __t_1."email", '__typename', 'User'::text) AS data
FROM "public"."users" AS __t_1
WHERE __t_1."id" = $1
LIMIT 1
-- args: [42]

