-- field: postById
SELECT json_build_object('author', __l_3.data) AS data
FROM "public"."posts" AS __t_1
LEFT JOIN LATERAL (
  SELECT json_build_object('email', __t_2."email", 'id', __t_2."id", 'fullName', __t_2."full_name") AS data
  FROM "public"."users" AS __t_2
  WHERE __t_2."id" = __t_1."author_id"
  LIMIT 1
) AS __l_3 ON true
WHERE __t_1."id" = $1
LIMIT 1
-- args: [1]

-- field: userById
SELECT json_build_object('address', CASE WHEN (__t_1."address")::text IS NULL THEN NULL ELSE json_build_object('city', (__t_1."address")."city", 'street', (__t_1."address")."street") END, 'postsByAuthorId', __l_5.data) AS data
FROM "public"."users" AS __t_1
LEFT JOIN LATERAL (
  SELECT json_build_object('nodes', coalesce(json_agg(__s_3.ndata ORDER BY __s_3.__rn ASC) FILTER (WHERE __s_3.__rn <= $1), '[]'::json), 'totalCount', (SELECT count(*) FROM "public"."posts" AS __c_4
  WHERE __c_4."author_id" = __t_1."id")::int) AS data
  FROM (
    SELECT json_build_object('id', __t_2."id", 'title', __t_2."title") AS ndata, row_number() OVER (ORDER BY __t_2."id" ASC) AS __rn
    FROM "public"."posts" AS __t_2
    WHERE __t_2."author_id" = __t_1."id"
    ORDER BY __t_2."id" ASC
    LIMIT 101
  ) AS __s_3
  GROUP BY ()
) AS __l_5 ON true
WHERE __t_1."id" = $2
LIMIT 1
-- args: [100,1]

