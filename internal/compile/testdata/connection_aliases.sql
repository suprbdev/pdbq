-- field: allPosts
SELECT json_build_object('count', (SELECT count(*) FROM "public"."posts" AS __c_3)::int, 'ids', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::json), 'titles', coalesce(json_agg(__s_2.ndata2 ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::json), 'edges', coalesce(json_agg(json_build_object('cursor', __s_2.__cursor, 'byId', __s_2.edata) ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::json), 'page', json_build_object('hasNextPage', (count(*) > $2)), '__typename', 'PostConnection'::text) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, json_build_object('title', __t_1."title") AS ndata2, json_build_object('id', __t_1."id") AS edata, replace(encode(convert_to(jsonb_build_array('Post'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', '') AS __cursor, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."posts" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT $1
) AS __s_2
GROUP BY ()
-- args: [3,2]

