-- field: node
SELECT jsonb_build_object('__typename', 'Post'::text, 'nodeId', replace(encode(convert_to(jsonb_build_array('Post'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', ''), 'title', __t_1."title", 'author', __l_3.data) AS data
FROM "public"."posts" AS __t_1
LEFT JOIN LATERAL (
  SELECT jsonb_build_object('id', __t_2."id", 'email', __t_2."email") AS data
  FROM "public"."users" AS __t_2
  WHERE __t_2."id" = __t_1."author_id"
  LIMIT 1
) AS __l_3 ON true
WHERE __t_1."id" = $1
LIMIT 1
-- args: ["1"]

