-- field: allUsers
SELECT json_build_object('totalCount', (SELECT count(*) FROM "public"."users" AS __c_3)::int, 'nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3), '[]'::json), 'edges', coalesce(json_agg(json_build_object('cursor', __s_2.__cursor, 'node', __s_2.edata) ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3), '[]'::json), 'pageInfo', json_build_object('hasNextPage', (count(*) > $3), 'hasPreviousPage', true, 'startCursor', (json_agg(__s_2.__cursor ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3))->0, 'endCursor', (json_agg(__s_2.__cursor ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3))->-1)) AS data
FROM (
  SELECT json_build_object('nodeId', replace(encode(convert_to(jsonb_build_array('User'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', ''), 'id', __t_1."id", 'email', __t_1."email") AS ndata, json_build_object('id', __t_1."id") AS edata, replace(encode(convert_to(jsonb_build_array('User'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', '') AS __cursor, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."users" AS __t_1
  WHERE __t_1."id" > $1
  ORDER BY __t_1."id" ASC
  LIMIT $2
) AS __s_2
GROUP BY ()
-- args: ["3",3,2]

