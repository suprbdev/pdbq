-- field: allUsers
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_3.ndata ORDER BY __s_3.__rn ASC) FILTER (WHERE __s_3.__rn <= $3), '[]'::jsonb), 'edges', coalesce(jsonb_agg(jsonb_build_object('cursor', __s_3.__cursor, 'node', __s_3.edata) ORDER BY __s_3.__rn ASC) FILTER (WHERE __s_3.__rn <= $3), '[]'::jsonb), 'totalCount', (SELECT count(*) FROM "public"."users" AS __c_4)::int, 'pageInfo', jsonb_build_object('hasNextPage', (count(*) > $3), 'hasPreviousPage', true, 'startCursor', (jsonb_agg(__s_3.__cursor ORDER BY __s_3.__rn ASC) FILTER (WHERE __s_3.__rn <= $3))->0, 'endCursor', (jsonb_agg(__s_3.__cursor ORDER BY __s_3.__rn ASC) FILTER (WHERE __s_3.__rn <= $3))->-1)) AS data
FROM (
  SELECT jsonb_build_object('nodeId', replace(encode(convert_to(jsonb_build_array('User'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', ''), 'id', __t_1."id", 'email', __t_1."email") AS ndata, jsonb_build_object('id', __t_1."id") AS edata, replace(encode(convert_to(jsonb_build_array('User'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', '') AS __cursor, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."users" AS __t_1
  CROSS JOIN (
    SELECT __a."id" AS k1
    FROM "public"."users" AS __a
    WHERE __a."id" = $1
  ) AS __anc_2
  WHERE __t_1."id" > __anc_2.k1
  ORDER BY __t_1."id" ASC
  LIMIT $2
) AS __s_3
GROUP BY ()
-- args: ["3",3,2]

