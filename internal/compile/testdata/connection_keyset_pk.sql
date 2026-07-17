-- field: allPosts
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3), '[]'::jsonb), 'pageInfo', jsonb_build_object('hasNextPage', (count(*) > $3), 'endCursor', (jsonb_agg(__s_2.__cursor ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3))->-1)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, replace(encode(convert_to(jsonb_build_array('Post'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', '') AS __cursor, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."posts" AS __t_1
  WHERE __t_1."id" > $1
  ORDER BY __t_1."id" ASC
  LIMIT $2
) AS __s_2
GROUP BY ()
-- args: ["3",4,3]

