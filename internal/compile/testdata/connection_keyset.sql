-- field: allPosts
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::jsonb), 'pageInfo', jsonb_build_object('hasNextPage', (count(*) > $4), 'endCursor', (jsonb_agg(__s_2.__cursor ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4))->-1)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'title', __t_1."title") AS ndata, replace(encode(convert_to(jsonb_build_array('Post'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', '') AS __cursor, row_number() OVER (ORDER BY __t_1."title" ASC, __t_1."author_id" DESC, __t_1."id" ASC) AS __rn
  FROM "public"."posts" AS __t_1
  WHERE (__t_1."title" > (SELECT __a."title" FROM "public"."posts" AS __a WHERE __a."id" = $1) OR (__t_1."title" = (SELECT __a."title" FROM "public"."posts" AS __a WHERE __a."id" = $1) AND (__t_1."author_id" < (SELECT __a."author_id" FROM "public"."posts" AS __a WHERE __a."id" = $1) OR (__t_1."author_id" = (SELECT __a."author_id" FROM "public"."posts" AS __a WHERE __a."id" = $1) AND __t_1."id" > $2))))
  ORDER BY __t_1."title" ASC, __t_1."author_id" DESC, __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["3","3",4,3]

