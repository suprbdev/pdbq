-- field: allMetrics
SELECT json_build_object('totalCount', (SELECT count(*) FROM "public"."metrics" AS __c_3)::int, 'nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC), '[]'::json), 'edges', coalesce(json_agg(json_build_object('cursor', encode(convert_to('o:' || (__s_2.__rn + 2)::text, 'UTF8'), 'base64'), 'node', __s_2.edata) ORDER BY __s_2.__rn ASC), '[]'::json), 'pageInfo', json_build_object('hasNextPage', ((SELECT count(*) FROM "public"."metrics" AS __c_3) > 2 + count(__s_2.__rn)), 'hasPreviousPage', (2 > 0), 'startCursor', (json_agg(encode(convert_to('o:' || (__s_2.__rn + 2)::text, 'UTF8'), 'base64') ORDER BY __s_2.__rn ASC))->0, 'endCursor', (json_agg(encode(convert_to('o:' || (__s_2.__rn + 2)::text, 'UTF8'), 'base64') ORDER BY __s_2.__rn ASC))->-1)) AS data
FROM (
  SELECT json_build_object('name', __t_1."name", 'value', __t_1."value") AS ndata, json_build_object('name', __t_1."name") AS edata, row_number() OVER () AS __rn
  FROM "public"."metrics" AS __t_1
  LIMIT 2
  OFFSET $1
) AS __s_2
GROUP BY ()
-- args: [2]

