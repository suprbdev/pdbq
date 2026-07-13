-- field: allMetrics
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC), '[]'::jsonb), 'edges', coalesce(jsonb_agg(jsonb_build_object('cursor', encode(convert_to('o:' || (__s_2.__rn + 2)::text, 'UTF8'), 'base64'), 'node', __s_2.edata) ORDER BY __s_2.__rn ASC), '[]'::jsonb), 'totalCount', (SELECT count(*) FROM "public"."metrics" AS __c_3)::int, 'pageInfo', jsonb_build_object('hasNextPage', ((SELECT count(*) FROM "public"."metrics" AS __c_3) > 2 + count(__s_2.__rn)), 'hasPreviousPage', (2 > 0), 'startCursor', (jsonb_agg(encode(convert_to('o:' || (__s_2.__rn + 2)::text, 'UTF8'), 'base64') ORDER BY __s_2.__rn ASC))->0, 'endCursor', (jsonb_agg(encode(convert_to('o:' || (__s_2.__rn + 2)::text, 'UTF8'), 'base64') ORDER BY __s_2.__rn ASC))->-1)) AS data
FROM (
  SELECT jsonb_build_object('name', __t_1."name", 'value', __t_1."value") AS ndata, jsonb_build_object('name', __t_1."name") AS edata, row_number() OVER () AS __rn
  FROM "public"."metrics" AS __t_1
  LIMIT $1
  OFFSET $2
) AS __s_2
GROUP BY ()
-- args: [2,2]

